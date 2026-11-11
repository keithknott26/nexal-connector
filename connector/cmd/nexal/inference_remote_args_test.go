package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The remote and setup commands must refuse malformed input from argument
// parsing alone, before reading config, secrets or the network.
func TestInferenceRemoteAndSetupRejectBadInputBeforeAnyIO(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	cases := []struct {
		name string
		run  func(context.Context, []string) error
		args []string
		want string
	}{
		{"remote-stop without id", inferenceRemote, []string{"remote-stop", "--config", cfg}, "usage: nexal inference remote-stop"},
		{"remote-list relative config", inferenceRemote, []string{"remote-list", "--config", "relative.json"}, "--config must be an absolute path"},
		{"remote-stats stray positional", inferenceRemote, []string{"remote-stats", "--config", cfg, "extra"}, "unexpected positional arguments"},
		{"remote-list unknown flag", inferenceRemote, []string{"remote-list", "--config", cfg, "--nope"}, "invalid command flags"},
		{"setup relative config", inferenceSetup, []string{"--config", "relative.json"}, "--config must be an absolute path"},
		{"setup stray positional", inferenceSetup, []string{"--config", cfg, "extra"}, "unexpected positional arguments"},
		{"setup unknown flag", inferenceSetup, []string{"--config", cfg, "--nope"}, "invalid command flags"},
	}
	for _, tc := range cases {
		err := tc.run(context.Background(), tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q, got %v", tc.name, tc.want, err)
		}
	}
}
