package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// securityImportCommand must refuse unusable input before reading config,
// the keychain or the network.
func TestSecurityImportCommandRejectsBadInputBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"relative input", []string{"--config", cfg, "--input", "capture.jsonl"}, "--input must be an absolute path"},
		{"missing input", []string{"--config", cfg}, "--input must be an absolute path"},
		{"unreadable input", []string{"--config", cfg, "--input", filepath.Join(dir, "absent.jsonl")}, "security capture unavailable"},
		{"directory input", []string{"--config", cfg, "--input", dir}, "regular file"},
		{"empty capture", []string{"--config", cfg, "--input", empty, "--source-id", "s", "--rules-version", "r"}, "capture is empty"},
		{"unknown flag", []string{"--nope"}, "invalid command flags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := securityImportCommand(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("securityImportCommand(%v) = %v, want error containing %q", tc.args, err, tc.want)
			}
		})
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("config file must not be created by a rejected import")
	}
}
