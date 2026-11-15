package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A status or cancel request for an enrollment session this Mac never started
// has no stored poll credential; both must fail with the fixed message rather
// than reaching the network or leaking secret-store details.
func TestStatusAndCancelPairV2RejectUnknownSessionCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secretsDir := t.TempDir()
	if err := os.Chmod(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXAL_SECRETS_DIR", secretsDir)
	path := savedUnenrolledConfig(t)
	const sid = "123e4567-e89b-12d3-a456-426614174000"
	var serr, cerr error
	out := captureStdout(t, func() {
		serr = statusPairV2(context.Background(), path, sid)
		cerr = cancelPairV2(context.Background(), path, sid)
	})
	for name, err := range map[string]error{"status": serr, "cancel": cerr} {
		if err == nil || !strings.Contains(err.Error(), "enrollment session credential unavailable") {
			t.Fatalf("%s error = %v, want credential unavailable", name, err)
		}
	}
	if out != "" {
		t.Fatalf("failed requests must not print success output: %q", out)
	}
}
