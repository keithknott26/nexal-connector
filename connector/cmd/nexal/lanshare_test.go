package main

import (
	"context"
	"strings"
	"testing"
)

func TestLanShareCommandRejectsMissingAndUnknownSubcommand(t *testing.T) {
	if err := lanShareCommand(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "usage: nexal lan-share") {
		t.Fatalf("missing subcommand not rejected with usage: %v", err)
	}
	if err := lanShareCommand(context.Background(), []string{"bogus"}); err == nil || !strings.Contains(err.Error(), `unknown lan-share subcommand "bogus"`) {
		t.Fatalf("unknown subcommand not rejected: %v", err)
	}
}

func TestLanShareDestinationBuildsURLWithoutPassword(t *testing.T) {
	out := captureStdout(t, func() {
		if err := lanShareCommand(context.Background(), []string{"destination", "--name", "Backups", "--host", "studio", "--user", "keith"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{`"destination":"smb://keith@studio/Backups"`, "tmutil setdestination -a smb://keith@studio/Backups", "NOT post-quantum"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestLanShareDestinationRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"destination", "--name", "Backups", "--host", "bad host", "--user", "k"},
		{"destination", "--name", "Backups", "--host", "studio", "extra"},
		{"destination", "--host", "studio"},
	} {
		if err := lanShareCommand(context.Background(), args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}
