package main

import (
	"context"
	"flag"
	"strings"
	"testing"

	"nexal/connector/internal/lanshare"
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

func TestLanShareDesiredRegistersPathAndNameFlags(t *testing.T) {
	fs := flag.NewFlagSet("lan-share", flag.ContinueOnError)
	path, name := lanShareDesired(fs)
	if err := fs.Parse([]string{"--path", "/Volumes/Data/Share", "--name", "Backups"}); err != nil {
		t.Fatal(err)
	}
	if *path != "/Volumes/Data/Share" || *name != "Backups" {
		t.Fatalf("flags not bound: path=%q name=%q", *path, *name)
	}
	fs = flag.NewFlagSet("lan-share", flag.ContinueOnError)
	path, name = lanShareDesired(fs)
	if err := fs.Parse(nil); err != nil || *path != "" || *name != "" {
		t.Fatalf("defaults should be empty: %q %q %v", *path, *name, err)
	}
}

func TestLanShareStatusAndSetupRejectStrayArgumentsBeforeTouchingTheHost(t *testing.T) {
	for _, tc := range []struct{ sub, usage string }{
		{"status", "unexpected positional arguments"},
		{"setup", "unexpected positional arguments"},
	} {
		err := lanShareCommand(context.Background(), []string{tc.sub, "--path", "/tmp/x", "--name", "X", "stray"})
		if err == nil || !strings.Contains(err.Error(), tc.usage) {
			t.Fatalf("%s accepted a stray positional argument: %v", tc.sub, err)
		}
	}
}

func TestLanShareStatusAndSetupReportMissingMacOSTools(t *testing.T) {
	if err := lanshare.Available(); err == nil {
		t.Skip("macOS sharing tools present; this test covers hosts without them")
	}
	for _, sub := range []string{"status", "setup"} {
		err := lanShareCommand(context.Background(), []string{sub, "--path", "/tmp/x", "--name", "X"})
		if err == nil || !strings.Contains(err.Error(), "requires macOS") {
			t.Fatalf("%s: want a requires-macOS error, got %v", sub, err)
		}
	}
}
