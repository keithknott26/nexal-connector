// Package remoteinference lets another Mac on the same neXal network ask this Mac to install and serve an AI
// model. The coordinator only queues the request; this runner performs it with the same local
// `nexal inference setup|install|serve` commands (and therefore the same owner policy, memory cap and
// SHA-256 pins) the owner would use by hand, and serves on this Mac's neXal address only.
package remoteinference

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Task is one queued request, as the coordinator reports it.
type Task struct {
	ID              string `json:"id"`
	RequesterHostID string `json:"requesterHostId"`
	TargetHostID    string `json:"targetHostId"`
	ModelID         string `json:"modelId"`
	State           string `json:"state"`
	Step            string `json:"step,omitempty"`
	Detail          string `json:"detail,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

// Report is the body of POST /api/v2/hosts/:id/remote-inference-state.
type Report struct {
	TaskID   string `json:"taskId"`
	State    string `json:"state"`
	Step     string `json:"step,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	// Progress is 0..1 while a download is under way; nil otherwise.
	Progress *float64 `json:"progress,omitempty"`
}

// Coordinator is the part of the API the runner uses.
type Coordinator interface {
	RemoteInferenceTasks(ctx context.Context, hostID string) ([]Task, error)
	ReportRemoteInference(ctx context.Context, hostID string, r Report) error
}

// Runner polls for requests addressed to this Mac.
type Runner struct {
	Coord      Coordinator
	HostID     string
	Exe        string   // the nexal binary
	BaseArgs   []string // e.g. --config <path>, appended to every inference command
	Address    func() string
	Port       int
	Poll       time.Duration
	ReadyAfter time.Duration
	Logger     *slog.Logger

	mu      sync.Mutex
	serving map[string]*serve // task id -> server
	working map[string]bool
}

type serve struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func (r *Runner) init() {
	if r.Poll == 0 {
		r.Poll = 15 * time.Second
	}
	if r.ReadyAfter == 0 {
		r.ReadyAfter = 6 * time.Minute
	}
	if r.Port == 0 {
		r.Port = 8080
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	r.serving = map[string]*serve{}
	r.working = map[string]bool{}
}

// Run blocks until ctx ends, then stops every server it started.
func (r *Runner) Run(ctx context.Context) {
	r.init()
	defer r.stopAll()
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.Once(ctx)
		t.Reset(r.Poll)
	}
}

// Once handles one poll. Exposed for tests.
func (r *Runner) Once(ctx context.Context) {
	if r.serving == nil {
		r.init()
	}
	tasks, err := r.Coord.RemoteInferenceTasks(ctx, r.HostID)
	if err != nil {
		r.Logger.Debug("remote inference poll failed", "error", err.Error())
		return
	}
	live := map[string]bool{}
	for _, t := range tasks {
		live[t.ID] = true
		r.mu.Lock()
		busy := r.working[t.ID] || r.serving[t.ID] != nil
		if !busy {
			r.working[t.ID] = true
		}
		r.mu.Unlock()
		if busy {
			continue
		}
		// pending, or running/ready from before a restart: do the work again (install is idempotent).
		go func(t Task) {
			defer func() { r.mu.Lock(); delete(r.working, t.ID); r.mu.Unlock() }()
			r.handle(ctx, t)
		}(t)
	}
	// A request that was cancelled or removed: stop its server.
	r.mu.Lock()
	for id, s := range r.serving {
		if !live[id] {
			s.cancel()
			delete(r.serving, id)
		}
	}
	r.mu.Unlock()
}

func (r *Runner) report(ctx context.Context, t Task, state, step, detail, endpoint string) {
	r.reportP(ctx, t, state, step, detail, endpoint, nil)
}

// reportP is report with the overall progress (0..1) of the whole request: runtime, check, download,
// verify, start. The app draws one bar from it, so every step moves it, not only the download.
func (r *Runner) reportP(ctx context.Context, t Task, state, step, detail, endpoint string, progress *float64) {
	detail = clean(detail, 280)
	if err := r.Coord.ReportRemoteInference(ctx, r.HostID, Report{TaskID: t.ID, State: state, Step: step, Detail: detail, Endpoint: endpoint, Progress: progress}); err != nil {
		r.Logger.Debug("remote inference report failed", "error", err.Error())
	}
}

func (r *Runner) fail(ctx context.Context, t Task, step, detail string) {
	r.Logger.Warn("remote inference failed", "model", t.ModelID, "step", step, "detail", clean(detail, 280))
	r.report(ctx, t, "failed", step, detail, "")
}

func (r *Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	all := append([]string{"inference"}, args...)
	all = append(all, r.BaseArgs...)
	return exec.CommandContext(ctx, r.Exe, all...)
}

func (r *Runner) handle(ctx context.Context, t Task) {
	addr := ""
	if r.Address != nil {
		addr, _, _ = strings.Cut(r.Address(), "/")
	}
	if addr == "" {
		r.fail(ctx, t, "network", "This Mac is not connected to the neXal network yet.")
		return
	}
	for _, step := range []struct {
		name, label string
		start       float64 // overall progress when the step begins
		args        []string
	}{
		{"setup", "Preparing the AI runtime", 0.02, []string{"setup"}},
		{"pin", "Checking " + t.ModelID, 0.08, []string{"pin", t.ModelID, "--latest"}},
		{"install", "Downloading " + t.ModelID, 0.12, []string{"install", t.ModelID, "--local"}},
	} {
		start := step.start
		// A model already on this Mac needs neither a new pin (which would re-read every file) nor a download.
		if (step.name == "pin" || step.name == "install") && r.installed(ctx, t.ModelID) {
			have := 0.85
			r.reportP(ctx, t, "running", step.name, t.ModelID+" is already on this Mac", "", &have)
			continue
		}
		r.reportP(ctx, t, "running", step.name, step.label, "", &start)
		if step.name == "install" {
			if msg := r.install(ctx, t, step.args); msg != "" {
				r.fail(ctx, t, step.name, msg)
				return
			}
			continue
		}
		if out, err := r.command(ctx, step.args...).CombinedOutput(); err != nil {
			r.fail(ctx, t, step.name, lastLine(string(out), err))
			return
		}
	}
	serveStart := 0.9
	r.reportP(ctx, t, "running", "serve", "Starting the model server (loading it into memory)", "", &serveStart)
	sctx, cancel := context.WithCancel(ctx)
	cmd := r.command(sctx, "serve", t.ModelID, "--host", addr, "--port", strconv.Itoa(r.Port))
	var tail lineTail
	cmd.Stdout, cmd.Stderr = &tail, &tail
	if err := cmd.Start(); err != nil {
		cancel()
		r.fail(ctx, t, "serve", err.Error())
		return
	}
	s := &serve{cmd: cmd, cancel: cancel}
	r.mu.Lock()
	r.serving[t.ID] = s
	r.mu.Unlock()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	endpoint := fmt.Sprintf("http://%s:%d/v1", addr, r.Port)
	deadline := time.Now().Add(r.ReadyAfter)
	for {
		select {
		case err := <-exited:
			r.drop(t.ID)
			if ctx.Err() == nil {
				r.fail(ctx, t, "serve", lastLine(tail.String(), err))
			}
			return
		case <-ctx.Done():
			cancel()
			return
		case <-time.After(2 * time.Second):
		}
		ok, why := up(ctx, endpoint)
		if ok {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			r.drop(t.ID)
			r.fail(ctx, t, "serve", fmt.Sprintf("The model did not start answering at %s (%s). Server output: %s", endpoint, clean(why, 80), clean(lastLine(tail.String(), errors.New("none")), 120)))
			return
		}
	}
	done := 1.0
	r.reportP(ctx, t, "ready", "serve", "Serving "+t.ModelID, endpoint, &done)
	// Keep watching: if the server dies later, say so.
	err := <-exited
	r.drop(t.ID)
	if ctx.Err() == nil && sctx.Err() == nil {
		r.fail(ctx, t, "serve", lastLine(tail.String(), err))
	}
}

// install runs `inference install --json` and reports the download's progress as it goes. It returns a
// failure message, or "" on success.
func (r *Runner) install(ctx context.Context, t Task, args []string) string {
	cmd := r.command(ctx, append(args, "--json")...)
	var errTail lineTail
	cmd.Stderr = &errTail
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err.Error()
	}
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	var failure string
	alreadyThere := false
	var last time.Time
	verifying := false
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var ev struct {
			Event        string `json:"event"`
			Code         string `json:"code"`
			Message      string `json:"message"`
			OverallBytes int64  `json:"overallBytes"`
			OverallTotal int64  `json:"overallTotal"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			_, _ = errTail.Write(append(append([]byte{}, sc.Bytes()...), '\n'))
			continue
		}
		switch ev.Event {
		case "error":
			if ev.Code == "already_installed" {
				alreadyThere = true
				continue
			}
			failure = ev.Message
		case "progress":
			if ev.OverallTotal <= 0 {
				continue
			}
			// The download fills 12%..82% of the whole request; verifying and starting take the rest.
			if ev.OverallBytes >= ev.OverallTotal {
				if !verifying {
					verifying = true
					v := 0.85
					r.reportP(ctx, t, "running", "verify", "Verifying the downloaded files", "", &v)
				}
				continue
			}
			if time.Since(last) < 2*time.Second {
				continue
			}
			last = time.Now()
			f := float64(ev.OverallBytes) / float64(ev.OverallTotal)
			overall := 0.12 + 0.70*f
			r.reportP(ctx, t, "running", "install", fmt.Sprintf("Downloading %s: %d%% (%s of %s)", t.ModelID, int(f*100), size(ev.OverallBytes), size(ev.OverallTotal)), "", &overall)
		}
	}
	if err := cmd.Wait(); err != nil {
		if alreadyThere && failure == "" {
			return "" // a race with another install of the same model: it is there, which is what we wanted
		}
		if failure != "" {
			return failure
		}
		return lastLine(errTail.String(), err)
	}
	return ""
}

func size(b int64) string {
	const g, m = 1 << 30, 1 << 20
	if b >= g {
		return fmt.Sprintf("%.1f GB", float64(b)/g)
	}
	return fmt.Sprintf("%d MB", b/m)
}

// installed reports whether `nexal inference status` lists the model as installed on this Mac.
func (r *Runner) installed(ctx context.Context, modelID string) bool {
	out, err := r.command(ctx, "status", "--json").Output()
	if err != nil {
		return false
	}
	var st struct {
		Models []struct {
			ModelID string `json:"modelId"`
		} `json:"models"`
	}
	if json.Unmarshal(out, &st) != nil {
		return false
	}
	for _, m := range st.Models {
		if m.ModelID == modelID {
			return true
		}
	}
	return false
}

func (r *Runner) drop(id string) {
	r.mu.Lock()
	delete(r.serving, id)
	r.mu.Unlock()
}

func (r *Runner) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.serving {
		s.cancel()
		delete(r.serving, id)
	}
}

func up(ctx context.Context, endpoint string) (bool, string) {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(c, http.MethodGet, endpoint+"/models", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

// clean makes text safe for the coordinator's single-line fields.
func clean(s string, max int) string {
	s = strings.Map(func(c rune) rune {
		if c < 32 || c == 127 {
			return ' '
		}
		return c
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		s = "failed"
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}

// lineTail keeps the end of a process's output.
type lineTail struct {
	mu sync.Mutex
	b  []byte
}

func (l *lineTail) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	if len(l.b) > 4096 {
		l.b = l.b[len(l.b)-4096:]
	}
	return len(p), nil
}

func (l *lineTail) String() string { l.mu.Lock(); defer l.mu.Unlock(); return string(l.b) }
