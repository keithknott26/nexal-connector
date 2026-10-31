package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
