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
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nexal/connector/internal/diaglog"
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
	// Endpoints is every address the model answers on: the neXal address first, then the LAN, then loopback.
	Endpoints []string `json:"endpoints,omitempty"`
	// Progress is the overall progress (0..1) the Mac doing the work last reported; nil before it has.
	Progress *float64 `json:"progress,omitempty"`
	// Pooling: this Mac is member PoolRank of PoolSize. PoolRing lists every member's ring address in
	// rank order once all have said where they listen; PoolEndpoint is rank 0's API once it answers.
	PoolID       string   `json:"poolId,omitempty"`
	PoolRank     int      `json:"poolRank,omitempty"`
	PoolSize     int      `json:"poolSize,omitempty"`
	PoolRing     []string `json:"poolRing,omitempty"`
	PoolEndpoint string   `json:"poolEndpoint,omitempty"`
	CreatedAt    string   `json:"createdAt,omitempty"`
	UpdatedAt    string   `json:"updatedAt,omitempty"`
}

// Report is the body of POST /api/v2/hosts/:id/remote-inference-state.
type Report struct {
	TaskID   string `json:"taskId"`
	State    string `json:"state"`
	Step     string `json:"step,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	// Endpoints lists every address the model answers on (neXal, LAN, loopback); sent once it is ready.
	Endpoints []string `json:"endpoints,omitempty"`
	// RingAddress is where this pool member listens for the other members (ip:port). Nil leaves what the
	// coordinator has; a pointer to "" takes this member out of the ring.
	RingAddress *string `json:"ringAddress,omitempty"`
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
	Coord    Coordinator
	HostID   string
	Exe      string   // the nexal binary
	BaseArgs []string // e.g. --config <path>, appended to every inference command
	Address  func() string
	// LAN lists this Mac's private LAN addresses; models also answer there (as well as on the neXal address
	// and loopback). nil means every private IPv4 address of an up, non-virtual interface. NoLAN turns it off.
	LAN        func() []string
	NoLAN      bool
	Port       int // first port to try for a server (default 8001)
	MaxPort    int // last port (default 8099): the neXal network only opens 8001-8099 between Macs
	Poll       time.Duration
	ReadyAfter time.Duration
	PoolWait   time.Duration // unused: a pool member waits for the others for as long as the request is open
	PoolPoll   time.Duration // how often a pool checks the coordinator and its servers (default 4 s)
	Heartbeat  time.Duration // how often a pool member says it is still there (default 15 s)
	FormWait   time.Duration // how long a new pool gets to start answering before it is given up (default 6 minutes)
	StallAfter time.Duration // a request that gets no bytes for this long means the pool is stuck (default 2 minutes)
	ProbeEvery time.Duration // how often an idle pool is asked for one token (default 10 s)
	RingGrace  time.Duration // how long a Mac may be missing from the ring before the pool counts it lost (default 15 s)
	WakeGrace  time.Duration // how long after waking from sleep ring and stall checks are held off (default 60 s)
	Rejoin     time.Duration // how long a member that left the pool waits before offering to join again (default 5 s)
	Logger     *slog.Logger
	LogDir     string // when set, each model server's output also goes to <LogDir>/inference-<port>.log
	// Lease opens (true) or closes (false) the storage-host lease: TCP 8001-8099 from the neXal storage host to
	// this Mac, and its network name there. The runner holds it while this Mac answers a request (a single serve,
	// or rank 0 of a pool), whichever helper is running, so a pool switching helpers never drops it.
	Lease func(ctx context.Context, open bool) error

	mu      sync.Mutex
	serving map[string]*serve // task id -> server
	working map[string]bool
	latest  map[string]Task // the last poll's requests for this Mac
	used    map[int]bool
	cursor  int // where the next freePort starts looking
	leased  bool
	leaseAt time.Time
}

// leaseRenew is how often the storage-host lease is renewed while held (the coordinator's lease lasts 10 minutes).
var leaseRenew = 3 * time.Minute

// holdLease opens or renews the storage-host lease while a request this Mac answers is running or ready, and
// closes it once none is. Called after every poll.
func (r *Runner) holdLease(ctx context.Context, tasks []Task) {
	if r.Lease == nil {
		return
	}
	want := false
	for _, t := range tasks {
		if (t.State == "running" || t.State == "ready") && (t.PoolID == "" || t.PoolRank == 0) {
			want = true
		}
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch {
	case want && (!r.leased || time.Since(r.leaseAt) >= leaseRenew):
		if err := r.Lease(lctx, true); err != nil {
			r.Logger.Warn("storage-host lease failed", "error", err.Error())
			return
		}
		r.leased, r.leaseAt = true, time.Now()
	case !want && r.leased:
		if err := r.Lease(lctx, false); err != nil {
			r.Logger.Debug("storage-host lease close failed", "error", err.Error())
		}
		r.leased = false
	}
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
		r.Port = 8001
	}
	if r.PoolWait == 0 {
		r.PoolWait = 10 * time.Minute
	}
	if r.PoolPoll == 0 {
		r.PoolPoll = 4 * time.Second
	}
	if r.Heartbeat == 0 {
		r.Heartbeat = 15 * time.Second
	}
	if r.FormWait == 0 {
		r.FormWait = 6 * time.Minute
	}
	if r.StallAfter == 0 {
		r.StallAfter = 2 * time.Minute
	}
	if r.ProbeEvery == 0 {
		r.ProbeEvery = 10 * time.Second
	}
	if r.RingGrace == 0 {
		r.RingGrace = 15 * time.Second
	}
	if r.WakeGrace == 0 {
		r.WakeGrace = 60 * time.Second
	}
	if r.Rejoin == 0 {
		r.Rejoin = 5 * time.Second
	}
	if r.MaxPort == 0 {
		r.MaxPort = 8099
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	r.serving = map[string]*serve{}
	r.working = map[string]bool{}
	r.latest = map[string]Task{}
	r.used = map[int]bool{}
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
	snapshot := map[string]Task{}
	for _, t := range tasks {
		snapshot[t.ID] = t
	}
	r.mu.Lock()
	r.latest = snapshot
	r.mu.Unlock()
	r.holdLease(ctx, tasks)
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
	cmd := exec.CommandContext(ctx, r.Exe, all...)
	// The helper ends when this agent does (see exitWithParent in cmd/nexal), so a crashed or restarted
	// agent never leaves a model server holding memory and a port.
	cmd.Env = append(os.Environ(), "NEXAL_EXIT_WITH_PARENT="+strconv.Itoa(os.Getpid()))
	// Stopping a helper asks it to end, so it takes its model server down with it instead of leaving it behind.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	return cmd
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
	setup := "setup"
	if t.PoolID != "" {
		setup = "pool-setup"
	}
	for _, step := range []struct {
		name, label string
		start       float64 // overall progress when the step begins
		args        []string
	}{
		{"setup", "Preparing the AI runtime", 0.02, []string{setup}},
		{"pin", "Checking " + t.ModelID, 0.08, nil}, // the reviewed version ships with the app: nothing to pin
		{"install", "Downloading " + t.ModelID, 0.12, []string{"install", t.ModelID, "--local"}},
	} {
		start := step.start
		// A model already on this Mac needs neither a new pin (which would re-read every file) nor a download.
		if step.name == "pin" {
			r.reportP(ctx, t, "running", step.name, "Using the reviewed version of "+t.ModelID, "", &start)
			continue
		}
		if step.name == "install" && r.installed(ctx, t.ModelID) {
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
	if t.PoolID != "" {
		r.pool(ctx, t, addr)
		return
	}
	// One Mac serving one model keeps the same port from run to run (the lowest free one), so callers
	// that saved its address keep working after a restart.
	port, perr := r.pickPort(addr, true)
	if perr != nil {
		r.fail(ctx, t, "serve", perr.Error())
		return
	}
	defer r.release(port)
	// The model server listens on loopback and a forwarder answers on the neXal address: a Mac cannot
	// connect to its own neXal address, so probing the server there would never succeed.
	lport, lerr := freeLoopbackPort()
	if lerr != nil {
		r.fail(ctx, t, "serve", lerr.Error())
		return
	}
	fwd, ferr := r.forwarder(ctx, addr, port)
	if ferr != nil {
		r.fail(ctx, t, "serve", fmt.Sprintf("Cannot answer on %s:%d: %v", addr, port, ferr))
		return
	}
	defer fwd.Close()
	local := net.JoinHostPort("127.0.0.1", strconv.Itoa(lport))
	serveStart := 0.9
	r.reportP(ctx, t, "running", "serve", "Starting the model server (loading it into memory)", "", &serveStart)
	sctx, cancel := context.WithCancel(ctx)
	cmd := r.command(sctx, "serve", t.ModelID, "--host", "127.0.0.1", "--port", strconv.Itoa(lport), "--storage-host=false")
	var tail lineTail
	cmd.Stdout, cmd.Stderr = &tail, &tail
	if lf := r.serverLog(port); lf != nil {
		defer lf.Close()
		w := io.MultiWriter(&tail, lf)
		cmd.Stdout, cmd.Stderr = w, w
		fmt.Fprintf(lf, "\n=== %s %s on port %d (task %s)\n", time.Now().Format(time.RFC3339), t.ModelID, port, t.ID)
	}
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
	endpoint := fmt.Sprintf("http://%s:%d/v1", addr, port)
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
		ok, why := up(ctx, "http://"+local+"/v1")
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
	fwd.SetUpstream(local, "")
	done := 1.0
	if err := r.Coord.ReportRemoteInference(ctx, r.HostID, Report{TaskID: t.ID, State: "ready", Step: "serve", Detail: "Serving " + t.ModelID,
		Endpoint: endpoint, Endpoints: fwd.Endpoints(), Progress: &done}); err != nil {
		r.Logger.Debug("remote inference report failed", "error", err.Error())
	}
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
			if ev.Code == "not_installable" {
				failure = "This model has not been reviewed for this version of neXal yet. Update neXal on that Mac, or pick another model."
			}
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

// lastLines is the last n non-empty lines of out joined with " | ", or "none".
func lastLines(out string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		return "none"
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " | ")
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

// freePort reserves a port in [Port, MaxPort] that nothing on addr is using. Successive calls move on
// through the range, so a ring address that was given up is not handed out again at once and a stale one
// cannot be mistaken for a new one.
func (r *Runner) freePort(addr string) (int, error) { return r.pickPort(addr, false) }

// serverLog opens <LogDir>/inference-<port>.log (capped at 5 MB, one rotation), or returns nil.
func (r *Runner) serverLog(port int) *diaglog.RotatingFile {
	if r.LogDir == "" || os.MkdirAll(r.LogDir, 0o700) != nil {
		return nil
	}
	return diaglog.NewRotatingFile(filepath.Join(r.LogDir, fmt.Sprintf("inference-%d.log", port)), 5<<20)
}

// pickPort is freePort; lowest starts at Port every time instead of after the last port handed out.
func (r *Runner) pickPort(addr string, lowest bool) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.MaxPort - r.Port + 1
	start := r.cursor
	if lowest {
		start = 0
	}
	for i := 0; i < n; i++ {
		p := r.Port + (start+i)%n
		if r.used[p] {
			continue
		}
		l, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(p)))
		if err != nil {
			continue
		}
		l.Close()
		// Servers also answer on loopback at the same port (this Mac cannot reach its own neXal address),
		// so the port must be free there too.
		if ip := net.ParseIP(addr); ip == nil || !ip.IsLoopback() {
			lo, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
			if err != nil {
				continue
			}
			lo.Close()
		}
		r.used[p] = true
		if !lowest {
			r.cursor = (p - r.Port + 1) % n
		}
		return p, nil
	}
	return 0, fmt.Errorf("no free port between %d and %d on this Mac", r.Port, r.MaxPort)
}

func (r *Runner) release(port int) {
	r.mu.Lock()
	delete(r.used, port)
	r.mu.Unlock()
}

// current is the latest poll's view of a request; ok is false once it was cancelled or removed.
func (r *Runner) current(id string) (Task, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.latest[id]
	return t, ok
}

// poolExit says how a pool server ended, with the last lines it printed: a crash alone ("segmentation
// fault") does not say what went wrong before it.
func poolExit(out string, err error) string {
	why := "the server exited"
	if err != nil {
		why = err.Error()
	}
	s := []rune(clean(why+". Server output: "+lastLines(out, 6), 1<<20))
	if len(s) > 280 {
		s = append([]rune("..."), s[len(s)-277:]...) // the end of the output says what went wrong
	}
	return string(s)
}

// forwarder answers on addr:port (the neXal address), loopback and this Mac's LAN addresses on the same port.
// A LAN address that cannot be used (taken port, address gone) is dropped rather than failing the model.
func (r *Runner) forwarder(ctx context.Context, addr string, port int) (*Forwarder, error) {
	p := strconv.Itoa(port)
	var base []string
	if ip := net.ParseIP(addr); ip == nil || !ip.IsLoopback() { // already loopback: a second listen would collide
		base = append(base, net.JoinHostPort("127.0.0.1", p))
	}
	var lan []string
	if !r.NoLAN {
		list := r.LAN
		if list == nil {
			list = lanAddresses
		}
		for _, a := range list() {
			if a != addr {
				lan = append(lan, net.JoinHostPort(a, p))
			}
		}
	}
	if len(lan) > 0 {
		f, err := NewForwarder(net.JoinHostPort(addr, p), append(base, lan...)...)
		if err == nil {
			r.Logger.InfoContext(ctx, "model also answers on the LAN", "addresses", lan)
			return f, nil
		}
		r.Logger.WarnContext(ctx, "cannot answer on the LAN; neXal address and loopback only", "error", err)
	}
	return NewForwarder(net.JoinHostPort(addr, p), base...)
}

// lanAddresses is every private IPv4 address (10/8, 172.16/12, 192.168/16) of an up interface that is not
// loopback, the neXal tunnel or a virtual machine bridge.
func lanAddresses() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		if n := in.Name; strings.HasPrefix(n, "utun") || strings.HasPrefix(n, "bridge") || strings.HasPrefix(n, "vmenet") ||
			strings.HasPrefix(n, "wt") || strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "veth") {
			continue
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
					out = append(out, ip4.String())
				}
			}
		}
	}
	return out
}
