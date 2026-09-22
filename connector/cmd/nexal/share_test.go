package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/smbshare"
)

// shareConfig writes a minimal valid configuration. development controls whether
// a non-445 port is permitted, which is the only thing this command reads the
// configuration for.
func shareConfig(t *testing.T, development bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	coordinator := "https://coordinator.example"
	if development {
		coordinator = "http://127.0.0.1:8787"
	}
	c := config.Config{Version: 1, Coordinator: coordinator, Name: "Friend's Mac", Listen: "127.0.0.1:8788",
		Development: development, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureShare runs one command with stdin supplied and stdout captured, the same
// technique the existing CLI tests use.
func captureShare(t *testing.T, stdin string, args []string) (string, error) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalIn, originalOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	go func() { _, _ = io.WriteString(inW, stdin); _ = inW.Close() }()
	runErr := run(context.Background(), args)
	os.Stdin, os.Stdout = originalIn, originalOut
	_ = outW.Close()
	output, readErr := io.ReadAll(outR)
	_ = outR.Close()
	_ = inR.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output), runErr
}

func TestShareStatusWithoutASessionIsHonestAndNamesTheRefusals(t *testing.T) {
	path := shareConfig(t, true)
	output, err := captureShare(t, "", []string{"share", "status", "--config", path})
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Recovery  bool     `json:"recovery"`
		Serving   bool     `json:"serving"`
		Refuses   []string `json:"refuses"`
		Transport string   `json:"transport"`
	}
	if json.Unmarshal([]byte(output), &report) != nil {
		t.Fatalf("status is not JSON: %q", output)
	}
	if report.Recovery || report.Serving {
		t.Fatal("status claimed a live recovery session where there is none")
	}
	// The three §12 names must appear, so an operator reading status learns what
	// a recovery session costs them.
	for _, capability := range []string{"jobs", "pool", "pager"} {
		found := false
		for _, refused := range report.Refuses {
			if refused == capability {
				found = true
			}
		}
		if !found {
			t.Fatalf("status does not say that %q is refused in recovery mode", capability)
		}
	}
	if !strings.Contains(strings.ToLower(report.Transport), "not post-quantum") {
		t.Fatal("status does not state that raw LAN SMB is not post-quantum")
	}
}

func TestShareStatusReportsAnExpiredSessionAsOver(t *testing.T) {
	path := shareConfig(t, true)
	// A session record left behind by a process that died: status must evaluate
	// expiry itself rather than believe the record.
	record := sessionRecord{SessionID: "abc", ShareName: "NexalRecovery", Username: "owner@example.test",
		Port: 445, StartedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour),
		TimeBox: "1h", Serves: []string{"smb-share"}, Refuses: []string{"jobs"},
		Transport:  smbshare.TransportNote,
		Supervisor: smbshare.Status{Configured: true, Serving: true}}
	if err := writeSession(path, record); err != nil {
		t.Fatal(err)
	}
	output, err := captureShare(t, "", []string{"share", "status", "--config", path})
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Recovery bool `json:"recovery"`
		Serving  bool `json:"serving"`
		Expired  bool `json:"expired"`
	}
	if json.Unmarshal([]byte(output), &report) != nil {
		t.Fatalf("status is not JSON: %q", output)
	}
	if report.Recovery || report.Serving || !report.Expired {
		t.Fatalf("a lapsed session was reported as live: %s", output)
	}
	// The record itself is private.
	st, err := os.Lstat(sessionPath(path))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("the session record is not a private 0600 file")
	}
}

func TestShareStopRequiresASessionAndWritesAPrivateSentinel(t *testing.T) {
	path := shareConfig(t, true)
	if _, err := captureShare(t, "", []string{"share", "stop", "--config", path}); err == nil {
		t.Fatal("stop succeeded with no session recorded")
	}
	if err := writeSession(path, sessionRecord{SessionID: "abc", ShareName: "NexalRecovery",
		Username: "owner@example.test", Port: 445, StartedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		TimeBox: "1h", Serves: []string{"smb-share"}, Transport: smbshare.TransportNote}); err != nil {
		t.Fatal(err)
	}
	if _, err := captureShare(t, "", []string{"share", "stop", "--config", path}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(stopPath(path))
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		t.Fatalf("the stop sentinel is not a private regular 0600 file: %v", err)
	}
}

func TestShareRefusesUnsafeInvocations(t *testing.T) {
	dev := shareConfig(t, true)
	prod := shareConfig(t, false)
	for name, args := range map[string][]string{
		"no_subcommand":   {"share"},
		"bad_subcommand":  {"share", "restart", "--config", dev},
		"positional":      {"share", "status", "--config", dev, "unexpected"},
		"relative_config": {"share", "status", "--config", "relative/path.json"},
		// A key in argv is readable from the process table, so there is no flag
		// for it and supplying one is an error whose message must not echo it.
		"key_in_argv":      {"share", "start", "--config", dev, "--image-key", "secret-must-not-echo"},
		"password_in_argv": {"share", "start", "--config", dev, "--password", "secret-must-not-echo"},
		"no_stdin_flag":    {"share", "start", "--config", dev, "--username", "owner@example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := captureShare(t, "", args)
			if err == nil {
				t.Fatal("unsafe invocation accepted")
			}
			if strings.Contains(err.Error(), "secret-must-not-echo") {
				t.Fatal("a secret argument was echoed back")
			}
		})
	}

	// A non-445 port is refused for a production configuration: recoveryOS will
	// only ever try 445, so anything else is a share the broken Mac cannot see.
	key := strings.Repeat("ab", 32)
	_, err := captureShare(t, key, []string{"share", "start", "--config", prod,
		"--port", "14450", "--username", "owner@example.test", "--image-key-stdin",
		"--path", filepath.Dir(prod), "--samba-binary", "/bin/sh", "--samba-sha256", strings.Repeat("0", 64),
		"--smbpasswd-binary", "/bin/cat", "--smbpasswd-sha256", strings.Repeat("0", 64),
		"--samba-version", "4.21.3", "--samba-architecture", runtime.GOARCH,
		"--samba-source-url", "https://download.samba.org/pub/samba/stable/samba-4.21.3.tar.gz",
		"--verification-method", "test"})
	if err == nil || !strings.Contains(err.Error(), "445") {
		t.Fatalf("a production share on a port recoveryOS cannot reach was accepted: %v", err)
	}

	// A malformed image key on stdin is refused before anything is launched.
	for _, stdin := range []string{"", "not-hex-at-all", strings.Repeat("ab", 16), strings.Repeat("zz", 32)} {
		_, err := captureShare(t, stdin, []string{"share", "start", "--config", dev,
			"--username", "owner@example.test", "--image-key-stdin", "--path", filepath.Dir(dev),
			"--samba-binary", "/bin/sh", "--samba-sha256", strings.Repeat("0", 64),
			"--smbpasswd-binary", "/bin/cat", "--smbpasswd-sha256", strings.Repeat("0", 64),
			"--samba-version", "4.21.3", "--samba-architecture", runtime.GOARCH,
			"--samba-source-url", "https://download.samba.org/pub/samba/stable/samba-4.21.3.tar.gz",
			"--verification-method", "test", "--port", "14450"})
		if err == nil {
			t.Fatalf("an unusable image key was accepted: %q", stdin)
		}
	}
}

// TestShareStartReachesTheScratchCheckWithEverythingElseSatisfied is as far as
// this command can be driven in CI.
//
// It proves the whole front half of `share start` works against pinned fixtures:
// flag parsing, the stdin-only image key, the configuration lock taken across
// read and write, provenance validation of BOTH binaries, the recovery mode
// starting, and the one-time code being minted. It then stops at the §9 scratch
// prerequisite, because the sandbox volume has a few GiB free and the floor is 32
// GiB — which is exactly the refusal that check exists for. Reaching THAT error
// and no earlier one is the assertion.
func TestShareStartReachesTheScratchCheckWithEverythingElseSatisfied(t *testing.T) {
	path := shareConfig(t, true)
	dir := t.TempDir()
	smbd := filepath.Join(dir, "smbd")
	smbpasswd := filepath.Join(dir, "smbpasswd")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Version 4.21.3'; exit 0; fi\nexit 0\n"
	passwd := script + "# smbpasswd fixture\n"
	if err := os.WriteFile(smbd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(smbpasswd, []byte(passwd), 0700); err != nil {
		t.Fatal(err)
	}
	smbdSum := sha256.Sum256([]byte(script))
	passwdSum := sha256.Sum256([]byte(passwd))
	served := filepath.Join(dir, "served")
	if err := os.MkdirAll(served, 0700); err != nil {
		t.Fatal(err)
	}
	output, err := captureShare(t, strings.Repeat("ab", 32)+"\n", []string{"share", "start", "--config", path,
		"--username", "owner@example.test", "--image-key-stdin", "--path", served,
		"--samba-binary", smbd, "--samba-sha256", hex.EncodeToString(smbdSum[:]),
		"--smbpasswd-binary", smbpasswd, "--smbpasswd-sha256", hex.EncodeToString(passwdSum[:]),
		"--samba-version", "4.21.3", "--samba-architecture", runtime.GOARCH,
		"--samba-source-url", "https://download.samba.org/pub/samba/stable/samba-4.21.3.tar.gz",
		"--verification-method", "fixture only; no publisher signature verified",
		"--port", "14450", "--time-box", "2s", "--max-size-gib", "64"})
	if err == nil {
		t.Fatal("a session started without the scratch space a streamed restore needs")
	}
	if !strings.Contains(err.Error(), "restore cache") {
		t.Fatalf("start failed before the scratch check: %v", err)
	}
	// The one-time code WAS printed to stdout before the supervisor refused, and
	// the session record must not have survived the failure.
	var record struct {
		OneTimeCode string `json:"oneTimeCode"`
		Refuses     []string
	}
	if json.Unmarshal([]byte(strings.SplitN(output, "\n", 2)[0]), &record) != nil {
		t.Fatalf("start did not emit JSON: %q", output)
	}
	if len(record.OneTimeCode) != smbshare.CodeLength {
		t.Fatalf("no one-time code was displayed: %q", output)
	}
	// And the code is nowhere on disk.
	for _, name := range []string{"smb-session.json", "smb.conf"} {
		b, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		if readErr == nil && strings.Contains(string(b), record.OneTimeCode) {
			t.Fatalf("the one-time code was written to %s", name)
		}
	}
}
