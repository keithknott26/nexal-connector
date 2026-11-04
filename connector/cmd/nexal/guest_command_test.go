package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// guestCommand must reject malformed invocations before touching the config,
// the keychain or the network.
func TestGuestCommandRejectsBadInvocations(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no action", nil, "guest requires"},
		{"unknown action", []string{"bogus", "--config", cfg}, "unknown guest action"},
		{"redeem without stdin flag", []string{"redeem", "--config", cfg}, "--code-stdin required"},
		{"relative config", []string{"status", "--config", "relative.json"}, "absolute path"},
		{"positional argument", []string{"status", "--config", cfg, "extra"}, "unexpected positional"},
		{"unknown flag", []string{"status", "--nope"}, "invalid command flags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guestCommand(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("guestCommand(%v) = %v, want error containing %q", tc.args, err, tc.want)
			}
		})
	}
}
