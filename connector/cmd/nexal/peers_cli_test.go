package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
	"nexal/connector/internal/peeridentity"
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

func TestSelfIdentityMissingConfigErrorsWithoutMintingAnything(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if _, err := selfIdentity(context.Background(), cfg); err == nil {
		t.Fatal("expected an error when no configuration exists")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("selfIdentity must not create files, found %d entries", len(entries))
	}
}

func TestSelfIdentityWithoutMintedIdentityPointsAtIdentityCommand(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "private", "config.json")
	seed := config.Config{Version: 1, Coordinator: "http://127.0.0.1:1", Name: "Mac", Listen: "127.0.0.1:8788",
		Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	if err := config.Save(cfg, seed); err != nil {
		t.Fatal(err)
	}
	private := filepath.Dir(cfg)
	before, err := os.ReadDir(private)
	if err != nil {
		t.Fatal(err)
	}
	_, err = selfIdentity(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "run `nexal identity` on this Mac first") {
		t.Fatalf("expected the identity hint, got %v", err)
	}
	if !errors.Is(err, peeridentity.ErrNoIdentity) {
		t.Fatalf("error must wrap ErrNoIdentity, got %v", err)
	}
	after, err := os.ReadDir(private)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("selfIdentity must not mint anything: %d -> %d entries", len(before), len(after))
	}
}
