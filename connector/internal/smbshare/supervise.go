package smbshare

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nexal/connector/internal/contribution"
)

// Status is the sanitized, operator-visible state of the share. It is the
// analogue of tunnel.Evidence and obeys the same rule: NO input, diagnostic or
// inherited value can promote it into verification, attestation or a
// post-quantum claim, and no raw Samba output is ever copied into it.
//
// There is no password field and never should be. The one-time share credential
// is displayed once by the CLI that minted it; a supervisor status is exactly
// the kind of structure that gets logged, persisted and shipped to a UI.
type Status struct {
	Configured  bool `json:"configured"`
	Serving     bool `json:"serving"`
	Quarantined bool `json:"quarantined"`
	// Verified/Attestation exist to be permanently false, for the same reason
	// tunnel.Evidence carries them: a reader who looks for proof must find an
	// explicit "no", not an absent field they can read as a yes.
	Verified           bool      `json:"verified"`
	Attestation        bool      `json:"attestation"`
	Version            string    `json:"version"`
	Architecture       string    `json:"architecture"`
	SHA256             string    `json:"sha256"`
	SourceURL          string    `json:"sourceUrl"`
	VerificationMethod string    `json:"verificationMethod"`
	ShareName          string    `json:"shareName,omitempty"`
	Username           string    `json:"username,omitempty"`
	Protocol           string    `json:"protocol"`
	Transport          string    `json:"transport"`
	TimeBox            string    `json:"timeBox,omitempty"`
	Deadline           time.Time `json:"deadline,omitzero"`
	LastObservation    time.Time `json:"lastObservation,omitzero"`
	LastHealthy        time.Time `json:"lastHealthy,omitzero"`
	StoppedReason      string    `json:"stoppedReason,omitempty"`
	Policy             string    `json:"policy"`
	VerificationGap    string    `json:"verificationGap"`
}

// Options carries the parts of supervision a test must be able to substitute and
// a caller must be able to see. It follows internal/tunnel in keeping launch
// policy in code (BuildArgs, CleanEnv, RenderConf) and leaving only observation
// injectable.
type Options struct {
	// Report receives every status transition. Never nil-checked away: the CLI
	// and the menu-bar app both need to see a quarantine as it happens.
	Report func(Status)
	// Password is the one-time share credential. Run provisions it into the
	// private passdb over a stdin pipe and ZEROES it before the share serves a
	// single byte, so the plaintext lives for milliseconds and only in RAM.
	Password *Password
	// Probe reports whether the share is answering. Nil means dial the
	// configured SMB port on loopback. A probe is REACHABILITY, not proof that
	// Time Machine accepted the destination; VerificationGap says so.
	Probe func(context.Context) error
	// HealthGrace is how long the process may fail the probe before the share is
	// declared broken and stopped. HealthInterval is the probe period.
	HealthGrace    time.Duration
	HealthInterval time.Duration
	// FreeSpace measures free bytes on the served volume. Nil means the real
	// contribution.FreeDiskBytes, which is what production uses; a test
	// substitutes it because a CI container has less free space than §9's floor
	// and would otherwise only ever reach the refusal.
	FreeSpace func(string) (uint64, error)
}

func (o Options) grace() time.Duration {
	if o.HealthGrace <= 0 {
		return 20 * time.Second
	}
	return o.HealthGrace
}

func (o Options) interval() time.Duration {
	if o.HealthInterval <= 0 {
		return 2 * time.Second
	}
	return o.HealthInterval
}

// downgradeMarkers are the fixed substrings that mean the supervised process is
// serving something weaker than the policy this package exists to enforce. Any
// one of them quarantines and stops the share; none of them can be cleared by a
// later healthy line, exactly as tunnel quarantine is sticky.
var downgradeMarkers = []string{
	"smb1", "nt1 ", "negotiated smb1", "protocol negotiated: nt1",
	"guest session", "mapped to guest", "anonymous session",
	"encryption disabled", "smb encrypt = off", "signing disabled",
	"panic", "internal error",
}

// Observe folds one bounded line of Samba diagnostics into Status.
//
// Samba's log lines are free text, not JSON, so the ONLY thing taken from them
// is a boolean decision. No substring of a diagnostic is ever copied into
// Status: Samba logs paths, usernames and occasionally authentication detail,
// and a status structure is a thing that gets persisted and shown.
func Observe(st Status, line []byte, now time.Time) Status {
	// No observation can make this verified, attested or post-quantum.
	st.Verified = false
	st.Attestation = false
	st.Transport = TransportNote
	if st.Quarantined {
		st.Serving = false
		return st
	}
	if len(line) > 64<<10 {
		// An unbounded diagnostic line is itself a reason to stop: it means the
		// supervised process is not behaving like the pinned build.
		st.Quarantined = true
		st.Serving = false
		st.StoppedReason = "samba emitted an oversize diagnostic line"
		st.LastObservation = now
		return st
	}
	low := strings.ToLower(string(line))
	for _, marker := range downgradeMarkers {
		if strings.Contains(low, marker) {
			st.Quarantined = true
			st.Serving = false
			st.StoppedReason = "samba reported a dialect, guest or encryption downgrade; no downgrade is permitted"
			st.LastObservation = now
			return st
		}
	}
	if strings.Contains(low, "waiting for connections") {
		st.LastObservation = now
	}
	return st
}

// preflightCleanup is §9's "refuse to start if cleanup cannot be guaranteed",
// made a real precondition instead of an intention. Everything the share will
// create lives under one private directory; if this process cannot create AND
// remove that directory right now, it will not be able to remove it later
// either, and a recovery session on someone else's Mac that cannot clean up
// after itself must not begin.
func preflightCleanup(privateDir string) error {
	state := stateDir(privateDir)
	if err := os.RemoveAll(state); err != nil {
		return errors.New("refusing to start: a previous recovery session's private state cannot be removed")
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return errors.New("refusing to start: cannot create private samba state directory")
	}
	st, err := os.Lstat(state)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("refusing to start: private samba state directory must be mode 0700 and not a symlink")
	}
	probe := filepath.Join(state, ".cleanup-probe")
	if err := os.WriteFile(probe, []byte("probe"), 0600); err != nil {
		return errors.New("refusing to start: private samba state directory is not writable")
	}
	if err := os.Remove(probe); err != nil {
		return errors.New("refusing to start: cannot guarantee removal of private samba state")
	}
	return nil
}

// cleanup removes every artifact this package created. A failure here is
// reported, never swallowed: leaving a passdb containing the share credential's
// hash on a friend's Mac is the exact outcome §9 forbids.
func cleanup(privateDir string) error {
	var failures []string
	if err := os.RemoveAll(stateDir(privateDir)); err != nil {
		failures = append(failures, "samba state")
	}
	if err := os.Remove(ConfPath(privateDir)); err != nil && !os.IsNotExist(err) {
		failures = append(failures, "generated smb.conf")
	}
	if len(failures) > 0 {
		return fmt.Errorf("recovery session could not clean up: %s left on this machine", strings.Join(failures, " and "))
	}
	return nil
}

// provision writes the one-time credential into the private passdb.
//
// THE PASSWORD NEVER TOUCHES ARGV OR THE ENVIRONMENT. It is written to
// smbpasswd's stdin (which `-s` requires) and the caller's copy is zeroed
// immediately afterwards, whether or not provisioning succeeded.
func provision(ctx context.Context, p Pin, confPath, username string, password *Password) error {
	if password == nil {
		return errors.New("a one-time share credential is required; the share never runs without one")
	}
	defer password.Zero()
	if err := password.Usable(); err != nil {
		return err
	}
	args, err := BuildPasswordArgs(confPath, username)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.PasswdBinary, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = CleanEnv()
	// smbpasswd -s reads the new password twice.
	secret := password.reveal()
	cmd.Stdin = strings.NewReader(secret + "\n" + secret + "\n")
	out := &limitWriter{limit: 8192}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		// The diagnostic is deliberately not echoed: smbpasswd can print the
		// account name and, on some failures, the input it saw.
		return errors.New("cannot provision the one-time share credential")
	}
	return nil
}

// Run supervises exactly one Samba process for exactly one time-boxed session.
//
// It fails closed on every axis: an unpinned or altered binary, an unsafe share,
// missing scratch space, a cleanup guarantee it cannot make, a downgrade
// diagnostic, a health probe that never succeeds, or an early process exit. The
// share stops when the time box elapses and there is no path to extend it.
//
// There is NO retry, NO second binary, NO fallback to SMB1 or to an unencrypted
// session, and NO automatic install or update.
func Run(ctx context.Context, p Pin, s Share, privateDir string, opts Options) error {
	if !filepath.IsAbs(privateDir) {
		return errors.New("absolute private directory required")
	}
	report := func(Status) {}
	if opts.Report != nil {
		report = opts.Report
	}
	st, err := Check(ctx, p)
	if err != nil {
		password := opts.Password
		if password != nil {
			password.Zero()
		}
		return err
	}
	if err = ValidateShare(s); err != nil {
		if opts.Password != nil {
			opts.Password.Zero()
		}
		return err
	}
	st.ShareName, st.Username = s.Name, s.Username
	st.TimeBox = s.TimeBox.String()
	// Scratch space BEFORE anything is written or launched (§9).
	measure := opts.FreeSpace
	if measure == nil {
		measure = contribution.FreeDiskBytes
	}
	if _, err = checkScratch(measure, s.Path, s.ScratchBytes); err != nil {
		if opts.Password != nil {
			opts.Password.Zero()
		}
		return err
	}
	if err = preflightCleanup(privateDir); err != nil {
		if opts.Password != nil {
			opts.Password.Zero()
		}
		return err
	}
	confPath, err := WriteConf(privateDir, s)
	if err != nil {
		if opts.Password != nil {
			opts.Password.Zero()
		}
		_ = cleanup(privateDir)
		return err
	}
	// provision always zeroes the caller's credential, success or failure.
	if err = provision(ctx, p, confPath, s.Username, opts.Password); err != nil {
		_ = cleanup(privateDir)
		return err
	}
	// Rehash immediately before launch rather than trusting the Check above: the
	// file that hashed correctly a moment ago is not necessarily the file about
	// to be executed.
	if err = Validate(p); err != nil {
		_ = cleanup(privateDir)
		return err
	}
	args, err := BuildArgs(confPath)
	if err != nil {
		_ = cleanup(privateDir)
		return err
	}

	// THE TIME BOX. A derived deadline, not a timer someone can reset: when it
	// fires the context cancels, CommandContext kills the process, and Run
	// returns. Recovery mode expiring is the normal, expected ending.
	runCtx, cancel := context.WithTimeout(ctx, s.TimeBox)
	defer cancel()
	deadline, _ := runCtx.Deadline()
	st.Deadline = deadline

	cmd := exec.CommandContext(runCtx, p.Binary, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = CleanEnv()
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err = cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = cleanup(privateDir)
		return errors.New("supervised samba process could not start")
	}
	started := time.Now()
	report(st)

	var mu sync.Mutex
	// mark runs under mu: the reader goroutine, the health goroutine and the
	// returning path all touch st.
	mark := func(f func(*Status)) Status { mu.Lock(); defer mu.Unlock(); f(&st); return st }

	diagnostics := make(chan struct{})
	go func() {
		defer close(diagnostics)
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 4096), 64<<10)
		for scan.Scan() {
			cur := mark(func(s *Status) { *s = Observe(*s, scan.Bytes(), time.Now()) })
			report(cur)
			if cur.Quarantined {
				cancel()
			}
		}
		if scan.Err() != nil {
			cur := mark(func(s *Status) {
				s.Quarantined = true
				s.Serving = false
				s.StoppedReason = "samba diagnostics could not be read; supervision cannot continue blind"
			})
			report(cur)
			cancel()
		}
		_ = reader.Close()
	}()

	probe := opts.Probe
	if probe == nil {
		probe = func(ctx context.Context) error {
			d := net.Dialer{Timeout: 2 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(s.Port)))
			if err != nil {
				return errors.New("share is not accepting connections")
			}
			return conn.Close()
		}
	}
	health := make(chan struct{})
	go func() {
		defer close(health)
		ticker := time.NewTicker(opts.interval())
		defer ticker.Stop()
		var lastHealthy time.Time
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			probeCtx, probeCancel := context.WithTimeout(runCtx, opts.interval())
			err := probe(probeCtx)
			probeCancel()
			now := time.Now()
			if err == nil {
				lastHealthy = now
				report(mark(func(s *Status) {
					if !s.Quarantined {
						s.Serving = true
						s.LastHealthy = now
					}
				}))
				continue
			}
			// Unhealthy. A share that has never answered within the grace
			// window, or has stopped answering for longer than it, is stopped:
			// a half-serving Time Machine destination silently corrupts a
			// backup run rather than failing it.
			reference := lastHealthy
			if reference.IsZero() {
				reference = started
			}
			if now.Sub(reference) < opts.grace() {
				continue
			}
			report(mark(func(s *Status) {
				s.Serving = false
				s.Quarantined = true
				s.StoppedReason = "share stopped answering its health probe within the grace window"
			}))
			cancel()
			return
		}
	}()

	waitErr := cmd.Wait()
	_ = writer.Close()
	<-diagnostics
	<-health

	// Cleanup runs on every exit path, and its failure is the error the caller
	// sees even when the session was otherwise clean.
	cleanupErr := cleanup(privateDir)
	final := mark(func(s *Status) {
		s.Serving = false
		if s.StoppedReason == "" {
			switch {
			case cleanupErr != nil:
				s.StoppedReason = "share stopped, but private state could not be removed"
			case runCtx.Err() != nil && ctx.Err() == nil:
				s.StoppedReason = "time box elapsed; the share is closed and the session is over"
			case ctx.Err() != nil:
				s.StoppedReason = "operator or host stopped the recovery session"
			case waitErr != nil:
				s.StoppedReason = "samba exited; no fallback share is started"
			default:
				s.StoppedReason = "samba exited unexpectedly with no diagnostic"
			}
		}
	})
	report(final)

	if cleanupErr != nil {
		return cleanupErr
	}
	if final.Quarantined {
		return errors.New("smb share policy violation: share quarantined and stopped; no downgrade permitted")
	}
	// The time box elapsing is success, and so is the operator stopping it.
	if runCtx.Err() != nil || ctx.Err() != nil {
		return nil
	}
	if waitErr != nil {
		return errors.New("supervised samba exited; the recovery share is closed and is not restarted")
	}
	return errors.New("supervised samba exited before its time box; the recovery share is closed")
}
