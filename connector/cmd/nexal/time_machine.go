package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/timemachine"
)

// Adding the gateway runs, as root:
//
//	sudo [-n] -- /usr/bin/script -q /dev/null /usr/bin/tmutil setdestination -a -p smb://user@host/share
//
// The SMB password is never in any argv (ps shows argv to every local user)
// and therefore never in sudo's command log. tmutil -p reads it at a
// non-echoing prompt from its terminal, so script(1) gives tmutil a
// pseudo-terminal as its controlling tty and forwards our stdin pipe into it.
// sudo authenticates on the user's own /dev/tty (we never pass -S), so it
// cannot consume the piped password; the password is only written after
// tmutil has printed its prompt, i.e. after echo is off.
const (
	sudoPath   = "/usr/bin/sudo"
	scriptPath = "/usr/bin/script"
	tmutilPath = "/usr/bin/tmutil"

	setDestinationTimeout = 3 * time.Minute
	tmutilOutputLimit     = 64 << 10
)

// runTmutil is the process seam; tests replace it. The stdin reader may block
// indefinitely, so the real runner does not wait for stdin copying to finish.
var runTmutil = func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// sudo relays SIGTERM to the root command; SIGKILL on sudo would orphan it.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _, _ = io.Copy(in, stdin); _ = in.Close() }()
	return cmd.Wait() // also closes the stdin pipe
}

// hasTerminal reports whether this process has a controlling terminal on which
// sudo can ask for the administrator password (the GUI app may run us without).
var hasTerminal = func() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func tmutilArgv(destinationURL string, interactive bool) []string {
	argv := []string{sudoPath}
	if !interactive {
		argv = append(argv, "-n") // fail instead of prompting when there is no terminal
	}
	return append(argv, "--", scriptPath, "-q", "/dev/null", tmutilPath, "setdestination", "-a", "-p", destinationURL)
}

// setDestination is replaceable in tests. destinationURL must be password-free;
// password is written to tmutil's prompt and cleared by the caller.
var setDestination = func(ctx context.Context, destinationURL string, password []byte) error {
	ctx, cancel := context.WithTimeout(ctx, setDestinationTimeout)
	defer cancel()
	interactive := hasTerminal()
	out := &promptWatcher{prompted: make(chan struct{})}
	feed := &passwordFeeder{prompted: out.prompted, closed: make(chan struct{}), secret: append(append(make([]byte, 0, len(password)+1), password...), '\n')}
	defer feed.Close()
	err := runTmutil(ctx, tmutilArgv(destinationURL, interactive), feed, out, os.Stderr)
	feed.Close()
	if err == nil {
		return nil
	}
	if detail := out.redacted(password); len(detail) > 0 {
		_, _ = os.Stderr.Write(append(detail, '\n'))
	}
	switch {
	case ctx.Err() != nil && !out.sawPrompt():
		return errors.New("tmutil setdestination timed out before tmutil asked for the share password; approve the administrator prompt in Terminal")
	case ctx.Err() != nil:
		return errors.New("tmutil setdestination timed out; check that the secure network is connected")
	case !interactive:
		return errors.New("tmutil setdestination needs administrator approval; run `nexal time-machine -connect` in Terminal")
	}
	return errors.New("tmutil setdestination failed; check that the secure network is connected, approve the administrator prompt, and grant Terminal Full Disk Access")
}

// promptWatcher captures (bounded) tmutil output from the pty and signals once
// tmutil has printed its password prompt.
type promptWatcher struct {
	mu       sync.Mutex
	buf      []byte
	prompted chan struct{}
	seen     bool
}

func (w *promptWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := tmutilOutputLimit - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(len(p), room)]...)
	}
	if !w.seen && looksLikePasswordPrompt(w.buf) {
		w.seen = true
		close(w.prompted)
	}
	return len(p), nil
}

func (w *promptWatcher) sawPrompt() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

// redacted returns the captured output with any occurrence of the password
// removed (defence in depth: echo is off when it is typed).
func (w *promptWatcher) redacted(password []byte) []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := bytes.TrimSpace(w.buf)
	if len(password) > 0 {
		out = bytes.ReplaceAll(out, password, []byte("[redacted]"))
	} else {
		out = bytes.Clone(out)
	}
	return out
}

// looksLikePasswordPrompt matches tmutil's non-echoing prompt: text mentioning
// "password", or any output that ends in a colon awaiting input.
func looksLikePasswordPrompt(b []byte) bool {
	if bytes.Contains(bytes.ToLower(b), []byte("password")) {
		return true
	}
	t := bytes.TrimRight(b, " \t")
	return len(t) > 0 && t[len(t)-1] == ':'
}

// passwordFeeder is the command's stdin. It yields nothing until the prompt
// is seen, then the password line exactly once, then EOF. The secret is
// cleared as soon as it is handed over or the feeder is closed.
type passwordFeeder struct {
	mu       sync.Mutex
	prompted <-chan struct{}
	closed   chan struct{}
	once     sync.Once
	secret   []byte
}

func (f *passwordFeeder) Read(p []byte) (int, error) {
	select {
	case <-f.prompted:
	case <-f.closed:
		return 0, io.EOF
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secret == nil {
		return 0, io.EOF
	}
	if len(p) < len(f.secret) {
		return 0, io.ErrShortBuffer
	}
	n := copy(p, f.secret)
	clear(f.secret)
	f.secret = nil
	return n, nil
}

func (f *passwordFeeder) Close() {
	f.once.Do(func() { close(f.closed) })
	f.mu.Lock()
	clear(f.secret)
	f.secret = nil
	f.mu.Unlock()
}

var meshLookup func(string) ([]string, error) // nil = system resolver

// timeMachineCommand reports readiness. With a gateway-client configuration and
// -connect it adds the operator gateway as a Time Machine destination.
// The legacy connector-hosted mode still fails closed (no JuiceFS metadata contract).
func timeMachineCommand(ctx context.Context, args []string) error {
	f, path, err := flags("time-machine")
	if err != nil {
		return err
	}
	connect := f.Bool("connect", false, "add the storage gateway as a Time Machine destination")
	dryRun := f.Bool("dry-run", false, "with -connect, validate everything but do not call tmutil")
	if err = parse(f, args, path); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !client.ValidID(cfg.HostID) {
		return errors.New("pair this computer before checking Time Machine")
	}
	secrets, err := config.NewSecrets(*path, cfg)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return err
	}
	api, err := client.New(cfg.Coordinator, token, cfg.Development)
	if err != nil {
		return err
	}
	clientCfg, err := api.TimeMachineClientConfig(ctx, cfg.HostID)
	if err != nil {
		return err
	}
	if clientCfg.Role == timemachine.RoleClient {
		return timeMachineClient(ctx, api, cfg.HostID, clientCfg, *connect, *dryRun)
	}
	if *connect {
		return errors.New("this network is not configured for gateway-backed Time Machine")
	}
	desired, err := api.TimeMachineConfig(ctx, cfg.HostID)
	if err != nil {
		return err
	}
	observation := timemachine.Observation{Platform: runtime.GOOS, LastErrorCode: "juicefs_metadata_unconfigured"}
	status := timemachine.Evaluate(desired, observation)
	// A disabled response has no error and stays disabled. An enabled response is
	// blocked before credentials are minted, so no unusable secret is created.
	if desired.Enabled {
		status.State = "blocked"
		status.DetailCode = "juicefs_metadata_unconfigured"
	}
	if err := api.ReportTimeMachineStatus(ctx, cfg.HostID, status); err != nil {
		return err
	}
	return emit(map[string]any{"timeMachine": status, "action": "Configure a tenant-scoped JuiceFS metadata service and signed privileged helper before enabling this host."})
}

func timeMachineClient(ctx context.Context, api *client.Client, hostID string, c timemachine.ClientConfig, connect, dryRun bool) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.Enabled {
		return emit(map[string]any{"timeMachine": map[string]any{"role": "client", "state": "disabled", "serviceState": c.ServiceState}})
	}
	d := *c.Destination
	view := map[string]any{"role": "client", "serviceState": c.ServiceState, "host": d.Host, "share": d.Share, "quotaBytes": c.QuotaBytes}
	if err := timemachine.ResolvesInsideMesh(d.Host, meshLookup); err != nil {
		view["state"] = "blocked"
		view["detail"] = err.Error()
		return emit(map[string]any{"timeMachine": view})
	}
	if !connect {
		view["state"] = "ready_to_connect"
		view["action"] = "Run: nexal time-machine -connect"
		return emit(map[string]any{"timeMachine": view})
	}
	if runtime.GOOS != "darwin" && !dryRun {
		return errors.New("Time Machine destinations can only be added on macOS")
	}
	credential, err := api.TimeMachineSMBCredential(ctx, hostID, d)
	if err != nil {
		return err
	}
	defer credential.Zero()
	view["destination"] = credential.RedactedURL()
	if dryRun {
		view["state"] = "validated"
		return emit(map[string]any{"timeMachine": view})
	}
	password := []byte(credential.Password)
	err = setDestination(ctx, credential.DestinationURL(), password)
	clear(password)
	if err != nil {
		return err
	}
	view["state"] = "destination_added"
	return emit(map[string]any{"timeMachine": view})
}
