package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

const bundleTestID = "11111111-1111-4111-8111-111111111111"

func TestBundleCommandRejectsBadFlagsBeforeReadingConfig(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	cases := []struct {
		command string
		args    []string
	}{
		{"bundle-send", nil},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "relative/dir", "--receiver-key-sha256", fingerprint}},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", "short"}},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", fingerprint, "--from-host", bundleTestID}},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", fingerprint, "--path-only"}},
		{"bundle-receive", nil},
		{"bundle-receive", []string{"--from-host", "bad id/.."}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--transfer", bundleTestID}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--bundle", "/tmp/b"}},
	}
	for _, tc := range cases {
		args := append(append([]string{}, tc.args...), "--config", "/private/tmp/nonexistent-nexal-test-config.json")
		err := bundleCommand(context.Background(), tc.command, args)
		if err == nil || !strings.Contains(err.Error(), tc.command+" requires") {
			t.Fatalf("%s %v: want flag validation error before config load, got %v", tc.command, tc.args, err)
		}
	}
}

func TestBundleCommandAcceptsValidFlagsThenFailsOnMissingConfig(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	missing := "/private/tmp/nonexistent-nexal-test-config.json"
	cases := []struct {
		command string
		args    []string
	}{
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", fingerprint, "--config", missing}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--config", missing}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--path-only", "--config", missing}},
	}
	for _, tc := range cases {
		err := bundleCommand(context.Background(), tc.command, tc.args)
		if err == nil {
			t.Fatalf("%s %v: want an error for the missing config", tc.command, tc.args)
		}
		if strings.Contains(err.Error(), tc.command+" requires") {
			t.Fatalf("%s %v: valid flags must pass validation, got %v", tc.command, tc.args, err)
		}
	}
}

func bundleTestConfig(t *testing.T, hostID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300, HostID: hostID,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBundleReceiveRequiresEnrolledHost(t *testing.T) {
	path := bundleTestConfig(t, "")
	err := bundleCommand(context.Background(), "bundle-receive", []string{"--from-host", bundleTestID, "--config", path})
	if err == nil || !strings.Contains(err.Error(), "enroll this Mac") {
		t.Fatalf("unenrolled host not refused before credential access: %v", err)
	}
}

func TestBundleReceiveRejectsSelfAsDonor(t *testing.T) {
	path := bundleTestConfig(t, bundleTestID)
	err := bundleCommand(context.Background(), "bundle-receive", []string{"--from-host", bundleTestID, "--config", path})
	if err == nil || !strings.Contains(err.Error(), "must be different enrolled hosts") {
		t.Fatalf("self-transfer not refused: %v", err)
	}
}
