package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeersCommandDispatchErrors(t *testing.T) {
	ctx := context.Background()
	if err := peersCommand(ctx, nil); err == nil || !strings.Contains(err.Error(), "usage: nexal peers") {
		t.Fatalf("no args: got %v", err)
	}
	if err := peersCommand(ctx, []string{"bogus"}); err == nil || !strings.Contains(err.Error(), `unknown peers subcommand "bogus"`) {
		t.Fatalf("unknown subcommand: got %v", err)
	}
}

func TestPeersRevokeRequiresFingerprintAndNoExtraArgs(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	err := peersCommand(ctx, []string{"revoke", "--config", cfg})
	if err == nil || !strings.Contains(err.Error(), "usage: nexal peers revoke") {
		t.Fatalf("missing fingerprint: got %v", err)
	}
	err = peersCommand(ctx, []string{"revoke", "--config", cfg, "--fingerprint", strings.Repeat("a", 64), "extra"})
	if err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("extra arg: got %v", err)
	}
}

func TestPeersListRejectsPositionalArgsAndRelativeConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	err := peersCommand(ctx, []string{"list", "--config", cfg, "extra"})
	if err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("positional arg: got %v", err)
	}
	if err := peersCommand(ctx, []string{"list", "--config", "relative.json"}); err == nil {
		t.Fatal("expected relative config path to be rejected")
	}
}

func TestPeersListEmptyRegistryReportsZeroMembers(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	var err error
	out := captureStdout(t, func() { err = peersCommand(context.Background(), []string{"list", "--config", cfg}) })
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, `"count":0`) || !strings.Contains(out, `"members":[]`) {
		t.Fatalf("expected an empty member list, got %q", out)
	}
}

func TestPeersRevokeUnknownFingerprintIsRefused(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	err := peersCommand(context.Background(), []string{"revoke", "--config", cfg, "--fingerprint", strings.Repeat("b", 64)})
	if err == nil {
		t.Fatal("revoking a peer that was never enrolled must fail, not report success")
	}
}
