package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// peers invite/prove/accept must reject malformed input before opening the
// identity or the registry, so a bad paste never creates state.
func TestPeersInviteProveAcceptRejectBadInput(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	fp := strings.Repeat("a", 64)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"invite without fingerprint", []string{"invite", "--config", cfg}, "usage: nexal peers invite"},
		{"invite with no roles", []string{"invite", "--config", cfg, "--fingerprint", fp, "--contributor=false"}, "at least one role"},
		{"prove without invitation", []string{"prove", "--config", cfg}, "usage: nexal peers prove"},
		{"prove with non-JSON", []string{"prove", "--config", cfg, "--invitation", "not json"}, "invitation is not valid"},
		{"prove with unknown field", []string{"prove", "--config", cfg, "--invitation", `{"bogus":1}`}, "invitation is not valid"},
		{"accept without proof", []string{"accept", "--config", cfg}, "usage: nexal peers accept"},
		{"accept with non-JSON", []string{"accept", "--config", cfg, "--proof", "nope"}, "proof is not valid"},
		{"accept with unknown field", []string{"accept", "--config", cfg, "--proof", `{"extra":1}`}, "proof is not valid"},
		{"accept with bad public key", []string{"accept", "--config", cfg, "--proof", `{"publicKey":"!!!"}`}, "publicKey is not valid base64"},
		{"accept with bad signature", []string{"accept", "--config", cfg, "--proof", `{"publicKey":"AAAA","signature":"!!!"}`}, "signature is not valid base64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := peersCommand(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("peers %v = %v, want error containing %q", tc.args, err, tc.want)
			}
		})
	}
}
