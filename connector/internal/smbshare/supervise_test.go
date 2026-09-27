package smbshare

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// healthy is the probe substituted for a real SMB reachability check. The real
// probe dials the configured port; no fixture listens on 445 (and nothing in CI
// may), so the supervision logic is exercised with the probe injected — the same
// technique internal/tunnel uses to exercise supervision without cloudflared.
func healthy(context.Context) error { return nil }

func unhealthy(context.Context) error { return errors.New("share is not accepting connections") }

// plenty stands in for the free-space measurement. The served volume in CI has a
// few GiB, which is below §9's 32 GiB floor, so the satisfied branch is only
// reachable with the measurement injected. TestRunRefusesWhenScratchSpaceIsMissing
// and TestScratchPrerequisiteIsCheckedUpFront cover the real refusal.
func plenty(string) (uint64, error) { return 512 << 30, nil }

// runFixture wires a fixture into Run with a short time box and a credential.
func runFixture(t *testing.T, f fixtureSet, opts Options) (Status, error) {
	t.Helper()
	privateDir := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(privateDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Report is called from the diagnostics reader, the health loop and the
	// returning path, so the test's own bookkeeping is locked.
	var mu sync.Mutex
	var last Status
	report := opts.Report
	opts.Report = func(st Status) {
		mu.Lock()
		last = st
		mu.Unlock()
		if report != nil {
			report(st)
		}
	}
	err := Run(context.Background(), f.pin, f.share, privateDir, opts)
	mu.Lock()
	final := last
	mu.Unlock()
	// Nothing this package created may survive the session.
	if _, statErr := os.Lstat(ConfPath(privateDir)); statErr == nil {
		t.Fatal("generated smb.conf survived the session")
	}
	if _, statErr := os.Lstat(stateDir(privateDir)); statErr == nil {
		t.Fatal("private samba state survived the session")
	}
	return final, err
}

func TestTimeBoxStopsTheShareAndCleansUp(t *testing.T) {
	f := fixture(t, "waiting for connections")
	f.share.TimeBox = 1500 * time.Millisecond
	password := NewPassword([]byte("A1B2C3"))
	start := time.Now()
	st, err := runFixture(t, f, Options{Password: password, Probe: healthy, FreeSpace: plenty,
		HealthInterval: 50 * time.Millisecond, HealthGrace: time.Second})
	// An elapsed time box is the NORMAL ending, so it is not an error.
	if err != nil {
		t.Fatalf("time box expiry reported as a failure: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("share outlived its time box by %s", elapsed)
	}
	if st.Serving || st.Quarantined || !strings.Contains(st.StoppedReason, "time box") {
		t.Fatalf("unexpected final status: %+v", st)
	}
	// The credential was consumed and zeroed before the share served anything.
	if !password.Zeroed() {
		t.Fatal("the one-time share credential was not zeroed after provisioning")
	}
	// It reached smbpasswd on STDIN and never through argv.
	record, err := os.ReadFile(f.record)
	if err != nil {
		t.Fatalf("smbpasswd fixture was not invoked: %v", err)
	}
	text := string(record)
	argv, stdin, ok := strings.Cut(text, "stdin:")
	if !ok {
		t.Fatalf("unexpected fixture record: %q", text)
	}
	if strings.Contains(argv, "A1B2C3") {
		t.Fatal("the share credential appeared in argv")
	}
	if !strings.Contains(stdin, "A1B2C3") {
		t.Fatal("the share credential did not reach smbpasswd on stdin")
	}
	if !strings.Contains(argv, "-s") || !strings.Contains(argv, "owner@example.test") {
		t.Fatalf("smbpasswd was not invoked in stdin mode for the share user: %q", argv)
	}
	// No status a caller could persist or display carries the credential.
	b, err := json.Marshal(st)
	if err != nil || strings.Contains(string(b), "A1B2C3") {
		t.Fatal("the share credential leaked into the supervisor status")
	}
}

func TestOperatorStopIsACleanEnding(t *testing.T) {
	f := fixture(t, "waiting for connections")
	f.share.TimeBox = time.Minute
	privateDir := filepath.Join(t.TempDir(), "private")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := make(chan struct{}, 1)
	err := Run(ctx, f.pin, f.share, privateDir, Options{Password: NewPassword([]byte("A1B2C3")),
		Probe: func(context.Context) error {
			select {
			case ready <- struct{}{}:
			default:
			}
			cancel()
			return nil
		}, FreeSpace: plenty, HealthInterval: 50 * time.Millisecond, HealthGrace: time.Second})
	cancel()
	select {
	case <-ready:
	default:
		t.Fatal("share never became ready")
	}
	if err != nil {
		t.Fatalf("operator stop reported as a failure: %v", err)
	}
	if _, statErr := os.Lstat(stateDir(privateDir)); statErr == nil {
		t.Fatal("private samba state survived an operator stop")
	}
}

func TestDowngradeDiagnosticsQuarantineAndStopTheShare(t *testing.T) {
	// Each line is something a Samba build could plausibly say and none of them
	// may be tolerated: a Time Machine destination that fell back to SMB1, to a
	// guest session or to an unencrypted session is not the thing the owner
	// consented to serve.
	for name, line := range map[string]string{
		"smb1_negotiated":     "protocol negotiated: NT1",
		"guest_mapping":       "user owner@example.test mapped to guest",
		"anonymous":           "anonymous session setup succeeded",
		"encryption_disabled": "session encryption disabled for this connection",
		"signing_disabled":    "server signing disabled",
		"panic":               "PANIC: internal error in smbd",
	} {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, line)
			f.share.TimeBox = 30 * time.Second
			st, err := runFixture(t, f, Options{Password: NewPassword([]byte("A1B2C3")),
				Probe: healthy, FreeSpace: plenty, HealthInterval: 50 * time.Millisecond, HealthGrace: 5 * time.Second})
			if err == nil {
				t.Fatal("a downgrade diagnostic did not stop the share")
			}
			if !st.Quarantined || st.Serving {
				t.Fatalf("supervisor failed open: %+v", st)
			}
			// The raw diagnostic must not be copied into the status: Samba logs
			// usernames, paths and occasionally authentication detail.
			b, _ := json.Marshal(st)
			if strings.Contains(string(b), line) {
				t.Fatal("raw samba diagnostics leaked into status")
			}
		})
	}
}

func TestQuarantineIsStickyAcrossLaterHealthyLines(t *testing.T) {
	st := Observe(Status{Configured: true, Serving: true}, []byte("protocol negotiated: NT1"), time.Now())
	if !st.Quarantined || st.Serving {
		t.Fatalf("downgrade tolerated: %+v", st)
	}
	st = Observe(st, []byte("smbd: waiting for connections"), time.Now())
	if !st.Quarantined || st.Serving {
		t.Fatal("a later healthy line cleared quarantine")
	}
	// An oversize line is itself a reason to stop supervising.
	st = Observe(Status{Configured: true}, []byte(strings.Repeat("x", (64<<10)+1)), time.Now())
	if !st.Quarantined {
		t.Fatal("an oversize diagnostic failed open")
	}
}

func TestShareThatNeverAnswersIsStopped(t *testing.T) {
	f := fixture(t, "waiting for connections")
	f.share.TimeBox = 30 * time.Second
	st, err := runFixture(t, f, Options{Password: NewPassword([]byte("A1B2C3")),
		Probe: unhealthy, FreeSpace: plenty, HealthInterval: 50 * time.Millisecond, HealthGrace: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("a share that never answered was left running")
	}
	if !st.Quarantined || st.Serving || !strings.Contains(st.StoppedReason, "health probe") {
		t.Fatalf("unexpected final status: %+v", st)
	}
}

func TestRunRefusesToStartWithoutAGuaranteedCleanupPath(t *testing.T) {
	f := fixture(t, "waiting for connections")
	// A private directory this process cannot write is a private directory whose
	// state it could not remove afterwards either, which §9 makes a refusal.
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.MkdirAll(readOnly, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0700) })
	password := NewPassword([]byte("A1B2C3"))
	if err := Run(context.Background(), f.pin, f.share, readOnly, Options{Password: password, Probe: healthy, FreeSpace: plenty}); err == nil {
		t.Fatal("started a session that could not clean up after itself")
	}
	// A refused start must not leave the credential in memory either.
	if !password.Zeroed() {
		t.Fatal("a refused start left the share credential unzeroed")
	}
}

func TestRunRefusesWithoutACredentialOrWithAnAlteredBinary(t *testing.T) {
	f := fixture(t, "waiting for connections")
	privateDir := filepath.Join(t.TempDir(), "private")
	if err := Run(context.Background(), f.pin, f.share, privateDir, Options{Probe: healthy, FreeSpace: plenty}); err == nil {
		t.Fatal("served a share with no one-time credential")
	}
	// Tampering between the pin being recorded and the launch must be caught.
	altered := fixture(t, "waiting for connections")
	writeExecutable(t, altered.pin.Binary, smbdScript("waiting for connections")+"# altered\n")
	password := NewPassword([]byte("A1B2C3"))
	if err := Run(context.Background(), altered.pin, altered.share, privateDir,
		Options{Password: password, Probe: healthy, FreeSpace: plenty}); err == nil {
		t.Fatal("launched a binary that no longer matched its pin")
	}
	if !password.Zeroed() {
		t.Fatal("a refused start left the share credential unzeroed")
	}
	if _, err := os.Lstat(stateDir(privateDir)); err == nil {
		t.Fatal("a refused start left private samba state behind")
	}
	if _, err := os.Lstat(privateDir); err == nil {
		// The directory itself may exist; the conf must not.
		if _, err := os.Lstat(ConfPath(privateDir)); err == nil {
			t.Fatal("a refused start left a generated smb.conf behind")
		}
	}
}

func TestRunRefusesWhenScratchSpaceIsMissing(t *testing.T) {
	f := fixture(t, "waiting for connections")
	// A requirement no volume in CI can satisfy stands in for the real case: the
	// check must happen before the process starts, not three hours in.
	f.share.ScratchBytes = 1 << 62
	password := NewPassword([]byte("A1B2C3"))
	err := Run(context.Background(), f.pin, f.share, filepath.Join(t.TempDir(), "private"),
		Options{Password: password, Probe: healthy, FreeSpace: plenty})
	if err == nil || !strings.Contains(err.Error(), "restore cache") {
		t.Fatalf("started a restore without the scratch space it needs: %v", err)
	}
	if !password.Zeroed() {
		t.Fatal("a refused start left the share credential unzeroed")
	}
	if _, err := os.Lstat(f.record); err == nil {
		t.Fatal("smbpasswd ran before the scratch prerequisite was checked")
	}
}
