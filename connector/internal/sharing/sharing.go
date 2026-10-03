// Package sharing reports whether this Mac's macOS sharing services are
// listening, and turns Remote Login and Screen Sharing on as root.
//
// WHY THIS EXISTS. When another of the owner's computers (or the iPhone) tries
// to reach Screen Sharing, Remote Login or File Sharing on this Mac while that
// service is off, the attempt simply fails: macOS has nothing listening and the
// owner is told nothing. The coordinator notices the refused attempt and routes
// a sharing.request to this Mac over the presence stream; this package is the
// half that can answer it, by observing the real state and (only when the owner
// has already said yes) switching a service on.
//
// WHAT IT IS NOT. Nothing here decides whether a service MAY be turned on. The
// owner's per-service opt-in lives on the coordinator and arrives as the
// autoApproved flag on a request; the agent is what reads it. Enable does what
// it is told, as root, and refuses to pretend otherwise:
//
//   - It refuses to run at all unless the process is uid 0, because the two
//     mechanisms it uses (systemsetup and launchctl bootstrap system) silently
//     do nothing useful for a non-root caller, and a command that "succeeded"
//     while changing nothing is the worst possible answer here.
//   - It verifies the result by probing the port rather than trusting an exit
//     status, because `launchctl bootstrap` returns 0 for a job that then fails
//     to start, and on recent macOS `systemsetup -setremotelogin` exits 0
//     without Full Disk Access while leaving SSH off.
//   - File Sharing is NOT supported. Turning the SMB daemon on without shared
//     folders and allowed users gives the owner a service that accepts
//     connections and then refuses every one of them, so this returns
//     ErrFileSharingManual and the UI sends the owner to System Settings.
//
// Probing is a short TCP connect to the loopback address and spawns nothing;
// enabling uses absolute paths, exec.CommandContext and no shell.
package sharing

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The three service identifiers, which are the coordinator's wire values and
// the keys of the map Status returns. They are part of the protocol: do not
// rename them.
const (
	ScreenSharing = "screen-sharing"
	FileSharing   = "file-sharing"
	SecureShell   = "secure-shell"
)

// Errors callers branch on.
var (
	// ErrFileSharingManual is File Sharing: macOS needs the owner to choose
	// which folders are shared and which users may connect, and neither choice
	// can be made for them.
	ErrFileSharingManual = errors.New("macOS needs you to choose the shared folders and users for File Sharing in System Settings > General > Sharing")
	// ErrNotRoot is Enable called by a process that is not uid 0.
	ErrNotRoot = errors.New("turning a sharing service on requires root")
	// ErrUnsupportedPlatform is Enable off macOS.
	ErrUnsupportedPlatform = errors.New("turning a sharing service on is macOS only")
	// ErrUnknownService is an identifier that is not one of the three above.
	ErrUnknownService = errors.New("unknown sharing service")
	// ErrNotListening is returned when every command succeeded but the port
	// never started accepting connections within EnableTimeout.
	ErrNotListening = errors.New("the service did not start listening; turn it on in System Settings > General > Sharing")
)

const (
	// EnableTimeout bounds how long Enable waits for the port to come up after
	// the commands have run. launchd starts these daemons in well under a
	// second when it is going to start them at all; ten seconds is slack for a
	// busy Mac, not an expectation.
	EnableTimeout = 10 * time.Second
	// enablePoll is the gap between port probes inside that window.
	enablePoll = 250 * time.Millisecond
	// commandTimeout bounds one privileged command.
	commandTimeout = 20 * time.Second
	// probeTimeout bounds one loopback connect. A service on this same machine
	// either answers immediately or is not there.
	probeTimeout = 300 * time.Millisecond
	// maxCommandOutput bounds how much of a failed command's output is kept for
	// the error message.
	maxCommandOutput = 300
)

// Services returns the three identifiers in a stable order.
func Services() []string { return []string{SecureShell, ScreenSharing, FileSharing} }

// Valid reports whether service is one of the three identifiers.
func Valid(service string) bool {
	switch service {
	case ScreenSharing, FileSharing, SecureShell:
		return true
	}
	return false
}

// Label is the owner-facing name of a service, for log and status detail text.
func Label(service string) string {
	switch service {
	case ScreenSharing:
		return "Screen Sharing"
	case FileSharing:
		return "File Sharing"
	case SecureShell:
		return "Remote Login"
	}
	return service
}

// port is the TCP port a listening service owns: SSH 22, Screen Sharing (VNC)
// 5900, File Sharing (SMB) 445.
func port(service string) string {
	switch service {
	case ScreenSharing:
		return "5900"
	case FileSharing:
		return "445"
	case SecureShell:
		return "22"
	}
	return ""
}

// The three seams tests replace. They are package variables for the same
// reason wol.systemInterfaces is: the real versions touch the process table and
// the network stack, and a unit test must be able to drive every branch without
// either. Production never reassigns them.
var (
	// listening reports whether something accepts TCP on 127.0.0.1:port.
	listening = func(ctx context.Context, tcpPort string) bool {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		d := net.Dialer{}
		conn, err := d.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", tcpPort))
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	// runRoot runs one privileged command and returns its output on failure.
	runRoot = func(ctx context.Context, name string, args ...string) error {
		ctx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		// WaitDelay so a child that keeps the pipe open cannot stall the
		// caller, and an EMPTY environment rather than the inherited one:
		// PATH is irrelevant because every program is an absolute path, but
		// DYLD_INSERT_LIBRARIES is not irrelevant, and inheriting it into a
		// root process is how a library gets loaded into one.
		cmd.WaitDelay = time.Second
		cmd.Env = []string{}
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		text := strings.TrimSpace(string(out))
		if len(text) > maxCommandOutput {
			text = text[:maxCommandOutput] + " ...(truncated)"
		}
		if text == "" {
			return err
		}
		return errors.New(text)
	}
	// euid is the effective user id, so the root check is testable.
	euid = os.Geteuid
	// platform is the operating system, so the macOS gate is testable.
	platform = runtime.GOOS
)

// Status reports which of the three services are listening right now, keyed by
// service identifier. All three keys are always present. It changes nothing and
// spawns nothing. The error is non-nil only when ctx ended before every probe
// had run, in which case the map is nil: a partial reading must not be
// mistaken for "these services are off".
func Status(ctx context.Context) (map[string]bool, error) {
	out := make(map[string]bool, 3)
	for _, service := range Services() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[service] = listening(ctx, port(service))
	}
	return out, nil
}

// Enable turns one service on as root and returns only once the port is
// actually accepting connections.
//
// secure-shell prefers /usr/sbin/systemsetup, which is the documented control
// and also records the owner's choice where System Settings shows it. That
// command needs Full Disk Access on recent macOS, so launchctl is the fallback:
// enable (clears the disabled override, which otherwise makes bootstrap a
// no-op) then bootstrap (loads the daemon now, so nothing waits for a reboot).
// screen-sharing has no systemsetup equivalent and uses the launchctl pair
// directly. file-sharing returns ErrFileSharingManual; see the package comment.
func Enable(ctx context.Context, service string) error {
	if !Valid(service) {
		return ErrUnknownService
	}
	if service == FileSharing {
		return ErrFileSharingManual
	}
	if platform != "darwin" {
		return ErrUnsupportedPlatform
	}
	if euid() != 0 {
		return ErrNotRoot
	}
	// Already listening: the commands below are idempotent, but running them
	// against a healthy daemon can bounce a live Screen Sharing session.
	if listening(ctx, port(service)) {
		return nil
	}
	var err error
	switch service {
	case SecureShell:
		if err = runRoot(ctx, "/usr/sbin/systemsetup", "-f", "-setremotelogin", "on"); err != nil {
			err = bootstrap(ctx, "system/com.openssh.sshd", "/System/Library/LaunchDaemons/ssh.plist")
		}
	case ScreenSharing:
		err = bootstrap(ctx, "system/com.apple.screensharing", "/System/Library/LaunchDaemons/com.apple.screensharing.plist")
	}
	// A failed command is reported ONLY if the port also never came up: both
	// launchctl subcommands exit nonzero for a job that is already loaded, which
	// is success as far as the owner is concerned.
	if waitListening(ctx, service) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrNotListening
}

// bootstrap clears the disabled override for a system daemon and loads it.
//
// The enable failing is not reported on its own: a daemon that was never
// disabled has no override to clear, and bootstrap alone then starts it. Only
// bootstrap's error is returned, and Enable discards even that when the port
// comes up anyway (bootstrap exits nonzero for an already-loaded job).
func bootstrap(ctx context.Context, label, plist string) error {
	_ = runRoot(ctx, "/bin/launchctl", "enable", label)
	return runRoot(ctx, "/bin/launchctl", "bootstrap", "system", plist)
}

// waitListening polls the service's port until it answers or EnableTimeout
// elapses. It returns false when ctx ends, which Enable reports as a failure
// rather than as success: a cancelled wait proves nothing.
func waitListening(ctx context.Context, service string) bool {
	deadline := time.Now().Add(EnableTimeout)
	for {
		if listening(ctx, port(service)) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		t := time.NewTimer(enablePoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
}

// SortedServices returns the keys of a status map in sorted order, for stable
// log and test output.
func SortedServices(state map[string]bool) []string {
	out := make([]string, 0, len(state))
	for service := range state {
		out = append(out, service)
	}
	sort.Strings(out)
	return out
}

// Control is the real implementation of the agent's sharing seam, so the agent
// depends on two small methods rather than on this package's globals.
type Control struct{}

func (Control) Status(ctx context.Context) (map[string]bool, error) { return Status(ctx) }
func (Control) Enable(ctx context.Context, service string) error    { return Enable(ctx, service) }
