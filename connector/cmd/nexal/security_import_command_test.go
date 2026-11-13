package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/config"
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

// A valid capture on a host that is not enrolled must stop before any
// keychain or network access and leave the capture untouched.
func TestSecurityImportCommandRequiresEnrolledHost(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300,
	}
	if err := config.Save(cfg, c); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "capture.jsonl")
	row := `{"event_type":"alert","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) +
		`","alert":{"signature_id":7,"severity":1},"src_ip":"192.168.1.2"}` + "\n"
	if err := os.WriteFile(capture, []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	err := securityImportCommand(context.Background(), []string{"--config", cfg, "--input", capture, "--source-id", "opaque_capture_7", "--rules-version", "rules_1"})
	if err == nil || !strings.Contains(err.Error(), "enroll this host") {
		t.Fatalf("unenrolled host: want enroll error, got %v", err)
	}
	if _, statErr := os.Stat(capture); statErr != nil {
		t.Fatalf("capture must be retained: %v", statErr)
	}
}
