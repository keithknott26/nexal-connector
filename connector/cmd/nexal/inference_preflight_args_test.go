package main

import (
	"context"
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
