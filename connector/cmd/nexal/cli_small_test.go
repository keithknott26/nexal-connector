package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddrListIsRepeatable(t *testing.T) {
	var a addrList
	if err := a.Set("10.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Set("10.0.0.2:2"); err != nil {
		t.Fatal(err)
	}
	if got := a.String(); got != "10.0.0.1:1,10.0.0.2:2" {
		t.Fatalf("String() = %q", got)
	}
}

func TestCurrentUsernamePrefersUserThenLogname(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("LOGNAME", "")
	if got := currentUsername(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	t.Setenv("LOGNAME", "login")
	if got := currentUsername(); got != "login" {
		t.Fatalf("expected LOGNAME fallback, got %q", got)
	}
	t.Setenv("USER", "alice")
	if got := currentUsername(); got != "alice" {
		t.Fatalf("expected USER precedence, got %q", got)
	}
}

func TestCanaryCommandRejectsBadInput(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	err := canaryCommand(context.Background(), []string{"--config", cfg, "--action", "explode"})
	if err == nil || !strings.Contains(err.Error(), "status, enable, or disable") {
		t.Fatalf("expected action error, got %v", err)
	}
	if err := canaryCommand(context.Background(), []string{"--config", "relative.json"}); err == nil {
		t.Fatal("expected relative config path to be rejected")
	}
}

func TestHoneypotCommandRejectsBadInput(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	err := honeypotCommand(context.Background(), []string{"--config", cfg, "--action", "explode"})
	if err == nil || !strings.Contains(err.Error(), "status, enable, or disable") {
		t.Fatalf("expected action error, got %v", err)
	}
	if err := honeypotCommand(context.Background(), []string{"--config", "relative.json"}); err == nil {
		t.Fatal("expected relative config path to be rejected")
	}
}

func TestBundleCommandValidatesFlagsBeforeLoadingConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	id := strings.Repeat("a", 32)
	cases := []struct {
		name    string
		command string
		args    []string
		want    string
	}{
		{"send missing flags", "bundle-send", nil, "bundle-send requires"},
		{"send relative bundle", "bundle-send", []string{"--transfer", id, "--bundle", "rel", "--receiver-key-sha256", strings.Repeat("b", 64)}, "bundle-send requires"},
		{"send short fingerprint", "bundle-send", []string{"--transfer", id, "--bundle", "/tmp/x", "--receiver-key-sha256", "abc"}, "bundle-send requires"},
		{"send rejects donor", "bundle-send", []string{"--from-host", id, "--transfer", id, "--bundle", "/tmp/x", "--receiver-key-sha256", strings.Repeat("b", 64)}, "bundle-send requires"},
		{"receive missing donor", "bundle-receive", nil, "bundle-receive requires"},
		{"receive rejects sender flags", "bundle-receive", []string{"--from-host", id, "--transfer", id}, "bundle-receive requires"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := bundleCommand(context.Background(), tc.command, append([]string{"--config", cfg}, tc.args...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}
