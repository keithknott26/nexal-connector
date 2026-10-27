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
