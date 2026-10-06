package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/mesh"
	"nexal/connector/internal/timemachine"
)

// Time Machine reads its share password from a controlling terminal. Expect
// allocates that terminal even when the app's stdin is a pipe or FIFO. The
// password travels through stdin only, after the non-echoing password prompt;
// it is never included in command arguments or the AppleScript source.
const (
	sudoPath      = "/usr/bin/sudo"
	expectPath    = "/usr/bin/expect"
	tmutilPath    = "/usr/bin/tmutil"
	osascriptPath = "/usr/bin/osascript"

	setDestinationTimeout = 3 * time.Minute
	tmutilOutputLimit     = 64 << 10
)

// tmTerminalScript runs only the explicitly supplied command. Command arguments
// are Tcl list elements, never evaluated as script. Hide all output after the
// password prompt so a child cannot accidentally echo the secret into logs.
const tmTerminalScript = `set timeout 150
spawn -noecho {*}$argv
expect {
    -nocase -re {password[^\r\n]*: ?$} {
        log_user 0
        if {[gets stdin secret] < 0} { exit 125 }
        send -- "$secret\r"
        unset secret
        expect { eof {} timeout { exit 124 } }
    }
    eof {}
    timeout { exit 124 }
}
set result [wait]
exit [lindex $result 3]
`

func tmExpectScript(args ...string) string {
	var command strings.Builder
	command.WriteString("set argv [list")
	for _, arg := range args {
		fmt.Fprintf(&command, " [encoding convertfrom utf-8 [binary format H* %x]]", []byte(arg))
	}
	command.WriteString("]\n")
	command.WriteString(tmTerminalScript)
	return command.String()
}

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

// elevatedPromptAvailable reports whether the macOS administrator dialog can be
// used when there is no terminal (tests turn it off).
var elevatedPromptAvailable = func() bool { return runtime.GOOS == "darwin" }

// elevatedPrompt is the text of the macOS administrator dialog.
const elevatedPrompt = "neXal-Connector wants to add your neXal Time Machine backup disk."

// shellQuote single-quotes s for /bin/sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// appleScriptString renders s as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// elevatedScript is the AppleScript run by osascript. It holds the
// password-free destination URL and the two FIFO paths, nothing secret.
func elevatedScript(destinationURL, inFIFO, outFIFO string) string {
	shell := fmt.Sprintf("%s -c %s < %s > %s 2>&1",
		expectPath, shellQuote(tmExpectScript(tmutilPath, "setdestination", "-a", "-p", destinationURL)), shellQuote(inFIFO), shellQuote(outFIFO))
	return fmt.Sprintf("do shell script %s with administrator privileges with prompt %s",
		appleScriptString(shell), appleScriptString(elevatedPrompt))
}

// errAdminCancelled is returned when the owner dismisses the macOS dialog.
var errAdminCancelled = errors.New("administrator approval was cancelled; the backup disk was not added")

// runElevated runs Expect+tmutil as root behind the macOS administrator
// dialog. The password never enters argv or the disk: Expect's stdin and its
// output are FIFOs in a private 0700 directory, tmutil's prompt is read back
// through the output FIFO, and only then is the password written to the input
// FIFO. Replaceable in tests.
var runElevated = func(ctx context.Context, destinationURL string, stdin io.Reader, stdout io.Writer) error {
	dir, err := os.MkdirTemp("", "nexal-tm-") // 0700
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	inFIFO, outFIFO := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	for _, p := range []string{inFIFO, outFIFO} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, osascriptPath, "-e", elevatedScript(destinationURL, inFIFO, outFIFO))
	var diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diag, &diag
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	output := make(chan struct{})
	go func() { // root's shell opens the output FIFO for writing
		defer close(output)
		if f, err := os.OpenFile(outFIFO, os.O_RDONLY, 0); err == nil {
			_, _ = io.Copy(stdout, f)
			_ = f.Close()
		}
	}()
	// ...and the input FIFO for reading. Not waited for, like runTmutil's
	// stdin copy: it ends when the caller closes the password feeder.
	go func() {
		if f, err := os.OpenFile(inFIFO, os.O_WRONLY, 0); err == nil {
			_, _ = io.Copy(f, stdin)
			_ = f.Close()
		}
	}()
	waitErr := cmd.Wait()
	// If the dialog was cancelled the shell never opened the FIFOs and the opens
	// above are still blocked. Opening the other end releases a blocked open;
	// retried briefly because a goroutine may not have reached its open yet.
	for deadline := time.Now().Add(3 * time.Second); ; {
		if f, err := os.OpenFile(outFIFO, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		if f, err := os.OpenFile(inFIFO, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		select {
		case <-output:
		case <-time.After(50 * time.Millisecond):
			if time.Now().Before(deadline) {
				continue
			}
		}
		break
	}
	if waitErr != nil && strings.Contains(diag.String(), "-128") {
		return errAdminCancelled
	}
	return waitErr
}

// destinationConfigured reports whether Time Machine already has this gateway
// share as a destination. `tmutil destinationinfo` needs no privileges and
// prints one "URL : smb://user@host/share" line per destination.
var destinationConfigured = func(host, share string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.Command(tmutilPath, "destinationinfo").Output()
	if err != nil {
		return false
	}
	return destinationListed(string(out), host, share)
}

// destinationVerifyWait bounds how long setup waits for Time Machine to list a
// newly added destination. Replaceable in tests.
var destinationVerifyWait = 10 * time.Second

func destinationAppeared(ctx context.Context, host, share string) bool {
	deadline := time.Now().Add(destinationVerifyWait)
	for {
		if destinationConfigured(host, share) {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func destinationListed(info, host, share string) bool {
	host, share = strings.ToLower(host), strings.ToLower(share)
	for _, line := range strings.Split(strings.ToLower(info), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "url" {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.Contains(value, "@"+host+"/") && strings.HasSuffix(strings.TrimRight(value, "/"), "/"+share) {
			return true
		}
	}
	return false
}

func tmutilArgv(destinationURL string, interactive bool) []string {
	argv := []string{sudoPath}
	if !interactive {
		argv = append(argv, "-n") // fail instead of prompting when there is no terminal
	}
	return append(argv, "--", expectPath, "-c", tmExpectScript(tmutilPath, "setdestination", "-a", "-p", destinationURL))
}

const timeMachineAccessError = "Time Machine reported that Full Disk Access is required for this operation. If neXal-Connector is already enabled, quit and reopen it before retrying; otherwise enable it in System Settings"

// A read-only probe in the connector's own process ancestry. No elevation,
// mounting (-m), credentials, or backup changes. An explicit denial blocks the
// admin prompt; other failures (including no backups) do not prove FDA denial.
var probeTimeMachineAccess = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, tmutilPath, "listbackups").CombinedOutput()
}

func preflightTimeMachineAccess(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, _ := probeTimeMachineAccess(ctx)
	if ctx.Err() != nil {
		return errors.New("Time Machine permission check timed out; no administrator approval was requested. Try again")
	}
	if bytes.Contains(bytes.ToLower(output), []byte("requires full disk access privileges")) {
		return errors.New(timeMachineAccessError)
	}
	return nil
}

// setDestination is replaceable in tests. destinationURL must be password-free;
// password is written to tmutil's prompt and cleared by the caller.
var setDestination = func(ctx context.Context, destinationURL string, password []byte) error {
	if err := preflightTimeMachineAccess(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, setDestinationTimeout)
	defer cancel()
	interactive := hasTerminal()
	out := &promptWatcher{prompted: make(chan struct{})}
	feed := &passwordFeeder{prompted: out.prompted, closed: make(chan struct{}), secret: append(append(make([]byte, 0, len(password)+1), password...), '\n')}
	defer feed.Close()
	elevated := !interactive && elevatedPromptAvailable()
	var err error
	if elevated {
		err = runElevated(ctx, destinationURL, feed, out)
	} else {
		err = runTmutil(ctx, tmutilArgv(destinationURL, interactive), feed, out, os.Stderr)
	}
	feed.Close()
	if err == nil {
		return nil
	}
	if errors.Is(err, errAdminCancelled) {
		return err
	}
	detail := out.redacted(password)
	if len(detail) > 0 {
		_, _ = os.Stderr.Write(append(detail, '\n'))
	}
	// Only a specific tmutil denial establishes missing effective permission.
	// Network, authentication, and other setup failures must not request FDA.
	if bytes.Contains(bytes.ToLower(detail), []byte("requires full disk access privileges")) {
		return errors.New(timeMachineAccessError)
	}
	switch {
	case elevated && ctx.Err() != nil && !out.sawPrompt():
		return errors.New("timed out waiting for administrator approval; the backup disk was not added")
	case elevated && ctx.Err() == nil:
		return errors.New("macOS did not add the backup disk; check the secure network and backup share availability, then retry")
	case ctx.Err() != nil && !out.sawPrompt():
		return errors.New("tmutil setdestination timed out before tmutil asked for the share password; approve the administrator prompt in Terminal")
	case ctx.Err() != nil:
		return errors.New("tmutil setdestination timed out; check that the secure network is connected")
	case !interactive:
		return errors.New("tmutil setdestination needs administrator approval; run `nexal time-machine -connect` in Terminal")
	}
	return errors.New("tmutil setdestination failed; check that the secure network and backup share are available")
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

var meshLookup func(string) ([]string, error) // nil = system resolver, then the mesh peer table

// peerTableLookup resolves a mesh name from the runtime's peer table. Replaceable in tests.
var peerTableLookup = func(host string) (string, bool) { return mesh.PeerAddressByName(context.Background(), host) }

// gatewayAddress is the address tmutil should dial for the gateway: the name itself when the
// system resolver answers with a mesh address, else the peer table's address for that name
// (macOS often has no resolver for *.peers.mesh.nexal.systems). The result is still checked
// to be inside 100.64.0.0/10 by ResolvesInsideMesh.
func gatewayAddress(host string) string {
	if meshLookup != nil {
		return host
	}
	if addrs, err := net.LookupHost(host); err == nil && len(addrs) > 0 {
		return host
	}
	if ip, ok := peerTableLookup(host); ok {
		return ip
	}
	return host
}

// timeMachineCommand reports readiness. With a gateway-client configuration and
// -connect it adds the operator gateway as a Time Machine destination.
// The legacy connector-hosted mode still fails closed (no JuiceFS metadata contract).
func timeMachineCommand(ctx context.Context, args []string) error {
	f, path, err := flags("time-machine")
	if err != nil {
		return err
	}
	reveal := f.Bool("reveal-credentials", false, "explicitly reveal this computer’s SMB credential for manual setup")
	connect := f.Bool("connect", false, "add the storage gateway as a Time Machine destination")
	dryRun := f.Bool("dry-run", false, "with -connect, validate everything but do not call tmutil")
	if err = parse(f, args, path); err != nil {
		return err
	}
	if *reveal && (*connect || *dryRun) {
		return errors.New("credential reveal cannot be combined with connect or dry-run")
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
	var clientCfg timemachine.ClientConfig
	if *connect && !*dryRun {
		clientCfg, err = enrollTimeMachine(ctx, api, cfg.HostID)
	} else {
		clientCfg, err = api.TimeMachineClientConfig(ctx, cfg.HostID)
	}
	if err != nil {
		return err
	}
	if *reveal {
		if err := clientCfg.Validate(); err != nil {
			return err
		}
		if clientCfg.Role != timemachine.RoleClient || !clientCfg.Enabled || clientCfg.Destination == nil {
			return errors.New("Time Machine is not enabled for this computer")
		}
		credential, err := api.TimeMachineSMBCredential(ctx, cfg.HostID, *clientCfg.Destination)
		if err != nil {
			return err
		}
		defer credential.Zero()
		// Only this explicit command emits a password; normal status stays redacted.
		return emit(map[string]string{"host": credential.Host, "share": credential.Share,
			"username": credential.Username, "password": credential.Password})
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

// How long -connect waits for a freshly enabled service to publish this
// computer's destination. Replaceable in tests.
var (
	enrollWait = 90 * time.Second
	enrollPoll = 3 * time.Second
)

// enrollTimeMachine is the "Set up" path: it asks the coordinator to turn backup
// on for this computer (provisioning the gateway share and its SMB account ahead
// of tmutil), then waits, bounded, until the destination is published. A
// coordinator that predates enrollment falls back to the plain config read.
func enrollTimeMachine(ctx context.Context, api *client.Client, hostID string) (timemachine.ClientConfig, error) {
	c, err := api.EnrollTimeMachine(ctx, hostID)
	if err != nil {
		var status *client.StatusError
		if !errors.As(err, &status) {
			return c, err
		}
		switch status.Code {
		case "paid_plan_required":
			return c, errors.New("Time Machine backup needs an active neXal subscription")
		case "feature_disabled":
			return c, errors.New("Time Machine backup is not available on this neXal server yet")
		case "time_machine_not_configured":
			return c, errors.New("pair this Mac to your neXal network before setting up Time Machine")
		}
		if c, err = api.TimeMachineClientConfig(ctx, hostID); err != nil {
			return c, err
		}
	}
	deadline := time.Now().Add(enrollWait)
	for c.Role == timemachine.RoleClient && (!c.Enabled || c.Destination == nil) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return c, ctx.Err()
		case <-time.After(enrollPoll):
		}
		if c, err = api.TimeMachineClientConfig(ctx, hostID); err != nil {
			return c, err
		}
	}
	if c.Role == timemachine.RoleClient && (!c.Enabled || c.Destination == nil) {
		return c, errors.New("neXal is still preparing your backup storage; try Set up again in a minute")
	}
	return c, nil
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
	// The address tmutil and the destination checks use: the gateway's name, or its mesh IP when
	// this Mac cannot resolve the name.
	dial := gatewayAddress(d.Host)
	if err := timemachine.ResolvesInsideMesh(dial, meshLookup); err != nil {
		view["state"] = "blocked"
		view["detail"] = err.Error()
		return emit(map[string]any{"timeMachine": view})
	}
	if !dryRun && (destinationConfigured(d.Host, d.Share) || (dial != d.Host && destinationConfigured(dial, d.Share))) {
		view["state"] = "connected"
		return emit(map[string]any{"timeMachine": view})
	}
	if !connect {
		view["state"] = "ready_to_connect"
		view["action"] = "Set up Time Machine in neXal-Connector, or run: nexal time-machine -connect"
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
	credential.Host = dial
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
	// tmutil can exit 0 without persisting the destination. Report success only
	// once Time Machine itself lists this gateway share.
	if !destinationAppeared(ctx, dial, d.Share) {
		return errors.New("macOS finished without adding the neXal backup disk to Time Machine; check the secure network and backup share availability, then retry")
	}
	view["state"] = "destination_added"
	return emit(map[string]any{"timeMachine": view})
}
