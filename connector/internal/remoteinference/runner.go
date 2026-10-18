// Package remoteinference lets another Mac on the same neXal network ask this Mac to install and serve an AI
// model. The coordinator only queues the request; this runner performs it with the same local
// `nexal inference setup|install|serve` commands (and therefore the same owner policy, memory cap and
// SHA-256 pins) the owner would use by hand, and serves on this Mac's neXal address only.
package remoteinference

import (
	"context"
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
		r.ReadyAfter = 3 * time.Minute
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
	detail = clean(detail, 280)
	if err := r.Coord.ReportRemoteInference(ctx, r.HostID, Report{TaskID: t.ID, State: state, Step: step, Detail: detail, Endpoint: endpoint}); err != nil {
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
		args        []string
	}{
		{"setup", "Preparing the AI runtime", []string{"setup"}},
		{"pin", "Checking " + t.ModelID, []string{"pin", t.ModelID, "--latest"}},
		{"install", "Downloading " + t.ModelID, []string{"install", t.ModelID, "--local"}},
	} {
		r.report(ctx, t, "running", step.name, step.label, "")
		if out, err := r.command(ctx, step.args...).CombinedOutput(); err != nil {
			r.fail(ctx, t, step.name, lastLine(string(out), err))
			return
		}
	}
	r.report(ctx, t, "running", "serve", "Starting the model server", "")
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
		if up(ctx, endpoint) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			r.drop(t.ID)
			r.fail(ctx, t, "serve", "The model did not start answering in time.")
			return
		}
	}
	r.report(ctx, t, "ready", "serve", "Serving "+t.ModelID, endpoint)
	// Keep watching: if the server dies later, say so.
	err := <-exited
	r.drop(t.ID)
	if ctx.Err() == nil && sctx.Err() == nil {
		r.fail(ctx, t, "serve", lastLine(tail.String(), err))
	}
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

func up(ctx context.Context, endpoint string) bool {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(c, http.MethodGet, endpoint+"/models", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
