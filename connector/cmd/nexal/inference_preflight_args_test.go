package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferencePreflightRejectsBadArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, "usage:"},
		{"wrong subcommand", []string{"serve"}, "usage:"},
		{"relative policy", []string{"preflight", "--config", "/tmp/nexal-x/config.json", "--runtime-owner-policy", "owner.json"}, "--runtime-owner-policy must be an absolute path"},
		{"relative config", []string{"preflight", "--config", "config.json"}, "--config must be an absolute path"},
		{"positional", []string{"preflight", "--config", "/tmp/nexal-x/config.json", "extra"}, "unexpected positional arguments"},
		{"unknown flag", []string{"preflight", "--nope"}, "invalid command flags"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := inferencePreflight(context.Background(), c.args)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want error containing %q", err, c.want)
			}
		})
	}
}

// A corrupt pins overlay must never break the read-only preflight: it falls back
// to the embedded catalog and reports the ignored overlay as a gap.
func TestInferencePreflightIgnoresBrokenPinsOverlay(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inference", "pins.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = inferencePreflight(context.Background(), []string{"preflight", "--json", "--config", filepath.Join(dir, "config.json")})
	})
	if runErr != nil {
		t.Fatalf("preflight with broken overlay failed: %v", runErr)
	}
	if !strings.Contains(out, "The pins overlay (inference/pins.json) was ignored") {
		t.Fatalf("report does not mention the ignored overlay: %s", out)
	}
}
