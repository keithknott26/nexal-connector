package diaglog

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestToggleRaisesLevelAndWritesDiagnosticsFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	logs := filepath.Join(dir, "logs")
	var stderr bytes.Buffer
	c := New(&stderr, 0 /* slog.LevelInfo */, cfg, logs)
	logger := c.Logger()

	logger.Debug("hidden before")
	c.Sync(logger)
	if c.On() || strings.Contains(stderr.String(), "hidden before") {
		t.Fatalf("debug record leaked while diagnostic mode is off: %q", stderr.String())
	}

	if err := SetEnabled(cfg, true); err != nil {
		t.Fatal(err)
	}
	if !Enabled(cfg) {
		t.Fatal("flag file not visible")
	}
	c.Sync(logger)
	if !c.On() {
		t.Fatal("diagnostic mode not applied from the flag file")
	}
	logger.With("component", "mesh").Debug("visible during", "peer", "m4")
	if !strings.Contains(stderr.String(), "visible during") {
		t.Fatalf("debug record missing from the ordinary log: %q", stderr.String())
	}
	diag, err := os.ReadFile(filepath.Join(logs, FileName))
	if err != nil || !strings.Contains(string(diag), "visible during") || !strings.Contains(string(diag), `"component":"mesh"`) {
		t.Fatalf("diagnostics file missing record: %q (%v)", diag, err)
	}

	if err := SetEnabled(cfg, false); err != nil {
		t.Fatal(err)
	}
	if err := SetEnabled(cfg, false); err != nil {
		t.Fatalf("turning off twice must be harmless: %v", err)
	}
	c.Sync(logger)
	if c.On() {
		t.Fatal("diagnostic mode still on after the flag was removed")
	}
	stderr.Reset()
	logger.Debug("hidden after")
	if strings.Contains(stderr.String(), "hidden after") {
		t.Fatal("debug record logged after diagnostic mode was turned off")
	}
}

func TestWatchStopsWithContext(t *testing.T) {
	dir := t.TempDir()
	c := New(&bytes.Buffer{}, 0, filepath.Join(dir, "config.json"), "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Watch(ctx, 10e6, c.Logger()); close(done) }()
	cancel()
	<-done
}

func TestRotatingFileKeepsOneRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.log")
	r := NewRotatingFile(p, 64)
	line := []byte(strings.Repeat("x", 40) + "\n")
	for i := 0; i < 5; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Close()
	if st, err := os.Stat(p); err != nil || st.Size() > 64 {
		t.Fatalf("current file not capped: %v %v", st, err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("rotation missing: %v", err)
	}
}

func TestRedactRemovesTokenLikeStrings(t *testing.T) {
	secrets := []string{
		"eyJhbGciOiJIUzI1NiJ9abcdefGHIJKL0123456789",
		"enr_9f8e7d6c5b4a",
		"s3cr3t-pass",
		"0E4F2A6B-1C3D-4E5F-8A9B-0C1D2E3F4A5B",
		"hunter2",
		"aa:bb:cc:dd:ee:01",
		"MC4CAQAwBQYDK2VwBCIEIA",
	}
	in := strings.Join([]string{
		`Authorization: Bearer eyJhbGciOiJIUzI1NiJ9abcdefGHIJKL0123456789`,
		`code enr_9f8e7d6c5b4a`,
		`{"setupKey":"0E4F2A6B-1C3D-4E5F-8A9B-0C1D2E3F4A5B","password":"s3cr3t-pass","credentialRejected":false}`,
		`smb://keith:hunter2@m4.nexal`,
		`mac aa:bb:cc:dd:ee:01`,
		"-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIA\n-----END PRIVATE KEY-----",
	}, "\n")
	out := Redact(in)
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("secret %q survived redaction:\n%s", s, out)
		}
	}
	for _, keep := range []string{`"credentialRejected":false`, "m4.nexal"} {
		if !strings.Contains(out, keep) {
			t.Errorf("harmless text %q was removed:\n%s", keep, out)
		}
	}
}

func TestTailStartsAtLineBoundary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	if err := os.WriteFile(p, []byte("first line\nsecond line\nthird\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Tail(p, 16); got != "third\n" {
		t.Fatalf("Tail = %q", got)
	}
	if Tail(filepath.Join(t.TempDir(), "missing"), 10) != "" {
		t.Fatal("missing file must yield empty tail")
	}
}

func TestSetEnabledIsIdempotentAndCreatesDirectory(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "nested", "config.json")
	if Enabled(cfg) {
		t.Fatal("flag must start off")
	}
	for range 2 {
		if err := SetEnabled(cfg, true); err != nil {
			t.Fatal(err)
		}
		if !Enabled(cfg) {
			t.Fatal("flag not set")
		}
	}
	st, err := os.Stat(FlagPath(cfg))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("flag file perm = %v, err = %v", st, err)
	}
	for range 2 {
		if err := SetEnabled(cfg, false); err != nil {
			t.Fatal(err)
		}
		if Enabled(cfg) {
			t.Fatal("flag not cleared")
		}
	}
}

func TestTailReturnsWholeSmallFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	if err := os.WriteFile(p, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Tail(p, 1024); got != "one\ntwo\n" {
		t.Fatalf("Tail = %q", got)
	}
}

func TestRedactLeavesOrdinaryLogTextAlone(t *testing.T) {
	in := `{"level":"INFO","msg":"peer connected","tokenCount":3,"host":"m4.nexal:7443"}`
	if out := Redact(in); out != in {
		t.Fatalf("ordinary text changed:\n%s", out)
	}
}

func TestRedactKeyValueFormAndLongRuns(t *testing.T) {
	out := Redact("api_key=abc123secret&mode=fast token=42 note=" + strings.Repeat("a", 40) + " blob=" + strings.Repeat("aB3", 14))
	for _, gone := range []string{"abc123secret", strings.Repeat("aB3", 14)} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived redaction: %s", gone, out)
		}
	}
	for _, keep := range []string{"mode=fast", "token=42", strings.Repeat("a", 40)} {
		if !strings.Contains(out, keep) {
			t.Errorf("harmless text %q was removed: %s", keep, out)
		}
	}
}

func TestTailWithoutNewlineKeepsTruncatedBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte("abcdefghij"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Tail(p, 4); got != "ghij" {
		t.Fatalf("Tail = %q", got)
	}
}

func TestLevelAndGroupedLoggerReachBothSinks(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	c := New(&stderr, slog.LevelWarn, filepath.Join(dir, "config.json"), filepath.Join(dir, "logs"))
	if got := c.Level(); got != slog.LevelWarn {
		t.Fatalf("baseline level = %v, want WARN", got)
	}
	c.Set(true)
	if got := c.Level(); got != slog.LevelDebug {
		t.Fatalf("diagnostic level = %v, want DEBUG", got)
	}

	c.Logger().WithGroup("mesh").With("peer", "m4").Debug("grouped", "state", "up")
	for name, out := range map[string]string{"ordinary log": stderr.String(), "diagnostics file": readDiag(t, dir)} {
		if !strings.Contains(out, `"mesh":{"peer":"m4","state":"up"}`) {
			t.Errorf("%s lost the group or attrs: %q", name, out)
		}
	}
}

func readDiag(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "logs", FileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLogDirIsUnderHomeAndPlatformSpecific(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := LogDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "nexal", "logs")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Library", "Logs", "Nexal")
	}
	if got != want {
		t.Fatalf("LogDir() = %q, want %q", got, want)
	}
}
