package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
	"nexal/connector/internal/drive"
)

func TestDriveCommandDispatchErrors(t *testing.T) {
	if err := driveCommand(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "usage: nexal drive") {
		t.Fatalf("no args error = %v", err)
	}
	if err := driveCommand(context.Background(), []string{"bogus"}); err == nil || !strings.Contains(err.Error(), `unknown drive subcommand "bogus"`) {
		t.Fatalf("unknown subcommand error = %v", err)
	}
}

func TestDriveKeyInfoReportsPathAndNeverPrintsKey(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	keyPath := filepath.Join(filepath.Dir(cfg), "drive-key.json")
	run := func() map[string]any {
		var runErr error
		out := captureStdout(t, func() { runErr = driveCommand(context.Background(), []string{"key", "--config", cfg}) })
		if runErr != nil {
			t.Fatal(runErr)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output not JSON: %q: %v", out, err)
		}
		return got
	}

	got := run()
	if got["path"] != keyPath || got["exists"] != false || got["escrowed"] != false {
		t.Fatalf("missing-key report = %v", got)
	}

	const secret = "SECRET-KEY-MATERIAL"
	if err := os.WriteFile(keyPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	got = run()
	if got["exists"] != true {
		t.Fatalf("existing-key report = %v", got)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), secret) {
		t.Fatal("drive key output leaked key material")
	}
}

func TestDriveKeyInfoRejectsRelativeConfig(t *testing.T) {
	if err := driveKeyInfo([]string{"--config", "relative.json"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative config error = %v", err)
	}
}

// Every refusal below happens before driveClient loads configuration or
// contacts the coordinator, so the test needs no enrolled host or network.
func TestDrivePutGetRefuseBadArgumentsBeforeAnyNetworkUse(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		run  func(context.Context, []string) error
		args []string
		want string
	}{
		{"put missing key", drivePut, []string{"--config", cfg, "--file", existing}, "both --key and --file are required"},
		{"put missing file", drivePut, []string{"--config", cfg, "--key", "a/b"}, "both --key and --file are required"},
		{"put relative file", drivePut, []string{"--config", cfg, "--key", "a/b", "--file", "rel.txt"}, "--file must be an absolute path"},
		{"put nonexistent file", drivePut, []string{"--config", cfg, "--key", "a/b", "--file", filepath.Join(dir, "nope")}, "cannot read"},
		{"put directory", drivePut, []string{"--config", cfg, "--key", "a/b", "--file", subdir}, "is a directory"},
		{"put positional", drivePut, []string{"--config", cfg, "extra"}, "unexpected positional arguments"},
		{"get missing out", driveGet, []string{"--config", cfg, "--key", "a/b"}, "both --key and --out are required"},
		{"get relative out", driveGet, []string{"--config", cfg, "--key", "a/b", "--out", "rel.txt"}, "--out must be an absolute path"},
		{"get refuses overwrite", driveGet, []string{"--config", cfg, "--key", "a/b", "--out", existing}, "already exists"},
		{"get relative config", driveGet, []string{"--config", "rel.json", "--key", "a/b", "--out", filepath.Join(dir, "new")}, "--config must be an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
	if b, err := os.ReadFile(existing); err != nil || string(b) != "keep" {
		t.Fatalf("existing file was modified: %q, %v", b, err)
	}
}

// list and usage share driveClient, so a bad invocation
// must fail before any credential lookup or network use.
func TestDriveListUsageRefuseBadInvocation(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	cases := []struct {
		name string
		run  func(context.Context, []string) error
		args []string
		want string
	}{
		{"list relative config", driveList, []string{"--config", "rel.json"}, "--config must be an absolute path"},
		{"list positional", driveList, []string{"--config", cfg, "extra"}, "unexpected positional arguments"},
		{"usage relative config", driveUsage, []string{"--config", "rel.json"}, "--config must be an absolute path"},
		{"usage positional", driveUsage, []string{"--config", cfg, "extra"}, "unexpected positional arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// Without a host credential every drive command must stop with the enrol hint
// rather than attempt a request.
func TestDriveCommandsRefuseWhenNotEnrolled(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "private", "config.json")
	seed := config.Config{Version: 1, Coordinator: "http://127.0.0.1:1", Name: "Mac", Listen: "127.0.0.1:8788",
		Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	if err := config.Save(cfg, seed); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func(context.Context, []string) error{
		"list": driveList, "usage": driveUsage,
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), []string{"--config", cfg})
			if err == nil || !strings.Contains(err.Error(), "not enrolled") {
				t.Fatalf("error = %v, want not-enrolled refusal", err)
			}
		})
	}
}

// An invalid key and an oversize file are both refused from the arguments and a
// stat alone: neither reads the file nor touches the configuration, which does
// not exist here.
func TestDrivePutRefusesInvalidKeyAndOversizeFileBeforeConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: Truncate allocates no data blocks.
	if err := f.Truncate(drive.MaxPlaintextBytes + 1); err != nil {
		f.Close()
		t.Skipf("cannot create sparse file: %v", err)
	}
	f.Close()

	err = drivePut(context.Background(), []string{"--config", cfg, "--key", "../escape", "--file", small})
	if err == nil || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "absolute") {
		t.Fatalf("invalid key should fail ValidateKey, got %v", err)
	}
	if err := drive.ValidateKey("../escape"); err == nil {
		t.Fatal("test premise broken: ../escape must be an invalid drive key")
	}

	err = drivePut(context.Background(), []string{"--config", cfg, "--key", "a/b", "--file", big})
	if err == nil || !strings.Contains(err.Error(), "Chunked upload is not implemented") {
		t.Fatalf("oversize file not refused on stat: %v", err)
	}
}

// An end-to-end pass through put, get, list and usage against a fake
// coordinator: the plaintext must never reach the wire, the restored file must
// match and be 0600, and the bearer must be the stored host credential.
func TestDriveEndToEndAgainstFakeCoordinator(t *testing.T) {
	var stored []byte
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/drive/objects/"):
			stored, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{
				"id": "1", "key": "docs/a.txt", "sizeBytes": len(stored), "createdAt": "2026-01-01T00:00:00Z"}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/drive/objects/"):
			_, _ = w.Write(stored)
		case r.URL.Path == "/api/drive/objects":
			_ = json.NewEncoder(w).Encode(map[string]any{"objects": []map[string]any{
				{"id": "1", "key": "docs/a.txt", "sizeBytes": len(stored), "createdAt": "2026-01-01T00:00:00Z"}}, "nextCursor": "c2"})
		case r.URL.Path == "/api/drive/usage":
			_ = json.NewEncoder(w).Encode(map[string]any{"tier": "free", "ceilingBytes": 100, "usedBytes": 150, "objectCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	seed := config.Config{Version: 1, Coordinator: srv.URL, Name: "Mac", Listen: "127.0.0.1:8788", HostID: "host-1",
		Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	if err := config.Save(cfg, seed); err != nil {
		t.Fatal(err)
	}
	if err := (config.FileSecrets{Dir: filepath.Join(dir, "dev-secrets")}).Put(context.Background(), "host", "tok1234567890abcdef"); err != nil {
		t.Fatal(err)
	}
	const plain = "very secret plaintext"
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}

	var err error
	out := captureStdout(t, func() {
		err = drivePut(context.Background(), []string{"--config", cfg, "--key", "docs/a.txt", "--file", src})
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if sawAuth != "Bearer tok1234567890abcdef" {
		t.Fatalf("bearer = %q", sawAuth)
	}
	if strings.Contains(string(stored), plain) || strings.Contains(out, plain) {
		t.Fatal("plaintext leaked to the coordinator or stdout")
	}

	dst := filepath.Join(dir, "restored.txt")
	captureStdout(t, func() {
		err = driveGet(context.Background(), []string{"--config", cfg, "--key", "docs/a.txt", "--out", dst})
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != plain {
		t.Fatalf("restored = %q", b)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o600 {
		t.Fatalf("restored mode = %v", st.Mode().Perm())
	}

	out = captureStdout(t, func() { err = driveList(context.Background(), []string{"--config", cfg}) })
	if err != nil || !strings.Contains(out, `"docs/a.txt"`) || !strings.Contains(out, `"nextCursor": "c2"`) && !strings.Contains(out, `"nextCursor":"c2"`) {
		t.Fatalf("list = %q, %v", out, err)
	}

	out = captureStdout(t, func() { err = driveUsage(context.Background(), []string{"--config", cfg}) })
	if err != nil {
		t.Fatal(err)
	}
	var usage map[string]any
	if json.Unmarshal([]byte(out), &usage) != nil || usage["remainingBytes"] != float64(0) {
		t.Fatalf("usage over ceiling must clamp remaining to 0: %q", out)
	}
}
