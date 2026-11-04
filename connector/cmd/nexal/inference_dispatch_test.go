package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferenceCommandRejectsMissingOrUnknownSubcommand(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"bogus"}, {""}} {
		err := inferenceCommand(context.Background(), args)
		if err == nil || !strings.HasPrefix(err.Error(), "usage: nexal inference preflight|setup") {
			t.Fatalf("args %q: expected usage error, got %v", args, err)
		}
	}
}

func TestInferencePreflightRejectsBadInputBeforeAnyIO(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"relative policy", []string{"preflight", "--config", cfg, "--runtime-owner-policy", "policy.json"}, "absolute path"},
		{"relative config", []string{"preflight", "--config", "relative.json"}, "--config must be an absolute path"},
		{"stray positional", []string{"preflight", "--config", cfg, "extra"}, "unexpected positional arguments"},
		{"unknown flag", []string{"preflight", "--config", cfg, "--nope"}, "invalid command flags"},
	}
	for _, tc := range cases {
		err := inferenceCommand(context.Background(), tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q, got %v", tc.name, tc.want, err)
		}
	}
}
