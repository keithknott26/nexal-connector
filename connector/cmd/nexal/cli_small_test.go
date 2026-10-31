package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
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
		{"send rejects path-only", "bundle-send", []string{"--transfer", id, "--bundle", "/tmp/x", "--receiver-key-sha256", strings.Repeat("b", 64), "--path-only"}, "bundle-send requires"},
		{"receive missing donor", "bundle-receive", nil, "bundle-receive requires"},
		{"receive rejects bundle", "bundle-receive", []string{"--from-host", id, "--bundle", "/tmp/x"}, "bundle-receive requires"},
		{"receive rejects fingerprint", "bundle-receive", []string{"--from-host", id, "--receiver-key-sha256", strings.Repeat("b", 64)}, "bundle-receive requires"},
		{"receive rejects malformed donor", "bundle-receive", []string{"--from-host", "bad/id"}, "bundle-receive requires"},
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

func decodeEmitted(t *testing.T, out string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, out)
	}
	return got
}

func TestCanaryCommandStatusEnableDisableReportsState(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	run := func(action string) map[string]any {
		var err error
		out := captureStdout(t, func() {
			err = canaryCommand(context.Background(), []string{"--config", cfg, "--action", action})
		})
		if err != nil {
			t.Fatalf("canary %s: %v", action, err)
		}
		return decodeEmitted(t, out)
	}
	if got := run("status"); got["enabled"] != false {
		t.Fatalf("fresh canary should be disabled: %v", got)
	}
	if got := run("enable"); got["enabled"] != true {
		t.Fatalf("enable should report enabled: %v", got)
	}
	got := run("disable")
	if got["enabled"] != false {
		t.Fatalf("disable should report disabled: %v", got)
	}
	if d, _ := got["description"].(string); !strings.Contains(d, "Does not scan documents") {
		t.Fatalf("description missing scope disclaimer: %v", got["description"])
	}
}

func TestHoneypotCommandStatusReportsEmptyRecentAsList(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	var err error
	out := captureStdout(t, func() {
		err = honeypotCommand(context.Background(), []string{"--config", cfg})
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeEmitted(t, out)
	if got["enabled"] != false {
		t.Fatalf("fresh honeypot should be disabled: %v", got)
	}
	if recent, ok := got["recent"].([]any); !ok || len(recent) != 0 {
		t.Fatalf("recent should be an empty list, not null: %#v", got["recent"])
	}
}

func TestIdentityCommandRejectsBadInputBeforeTouchingKeys(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--config", cfg, "--rotate"}, "invalid command flags"},
		{"positional argument", []string{"--config", cfg, "extra"}, "unexpected positional arguments"},
		{"relative config", []string{"--config", "relative.json"}, "absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := identityCommand(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
	// A rejected invocation must not have created the config file or its lock.
	if entries, _ := filepath.Glob(cfg + "*"); len(entries) != 0 {
		t.Fatalf("rejected identity call created files: %v", entries)
	}
}

func TestBundleCommandRejectsUnenrolledHostAndSelfDonor(t *testing.T) {
	id := strings.Repeat("a", 32)
	save := func(t *testing.T, hostID string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "private", "config.json")
		c := config.Config{Version: 1, Coordinator: "http://127.0.0.1:1", Name: "Mac", HostID: hostID, Listen: "127.0.0.1:8788",
			Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
		if err := config.Save(path, c); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Run("unenrolled host", func(t *testing.T) {
		err := bundleCommand(context.Background(), "bundle-receive", []string{"--config", save(t, ""), "--from-host", id})
		if err == nil || !strings.Contains(err.Error(), "enroll this Mac") {
			t.Fatalf("expected enrollment error, got %v", err)
		}
	})
	t.Run("donor equals receiver", func(t *testing.T) {
		err := bundleCommand(context.Background(), "bundle-receive", []string{"--config", save(t, id), "--from-host", id})
		if err == nil || !strings.Contains(err.Error(), "must be different") {
			t.Fatalf("expected distinct-host error, got %v", err)
		}
	})
}

func TestIdentityCommandCreatesOnceThenReusesFingerprint(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "private", "config.json")
	seed := config.Config{Version: 1, Coordinator: "http://127.0.0.1:1", Name: "Mac", Listen: "127.0.0.1:8788",
		Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	if err := config.Save(cfg, seed); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) string {
		var err error
		out := captureStdout(t, func() {
			err = identityCommand(context.Background(), append([]string{"--config", cfg}, extra...))
		})
		if err != nil {
			t.Fatalf("identity %v: %v", extra, err)
		}
		return out
	}
	first := decodeEmitted(t, run())
	fp, _ := first["deviceFingerprint"].(string)
	if len(fp) != 64 || first["created"] != true {
		t.Fatalf("first run should create a 64-hex fingerprint: %v", first)
	}
	if note, ok := first["privateKey"].(string); !ok || strings.Contains(note, fp) {
		t.Fatalf("privateKey field must be a description, never key material: %v", first)
	}
	second := decodeEmitted(t, run())
	if second["deviceFingerprint"] != fp || second["created"] != false {
		t.Fatalf("second run must reuse the pinned fingerprint: %v", second)
	}
	if got := strings.TrimSpace(run("--fingerprint-only")); got != `"`+fp+`"` {
		t.Fatalf("--fingerprint-only = %s, want %q", got, fp)
	}
	c, err := config.Load(cfg)
	if err != nil || c.Discovery == nil || c.Discovery.DeviceFingerprint != fp {
		t.Fatalf("fingerprint not persisted to config: %v %+v", err, c.Discovery)
	}
}
