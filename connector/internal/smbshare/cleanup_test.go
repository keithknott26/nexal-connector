package smbshare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightCleanupRefusesWhenStateDirIsUnusable(t *testing.T) {
	// A regular file where the private directory should be makes state setup fail
	// regardless of the caller's privileges (root ignores mode bits).
	blocker := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	err := preflightCleanup(blocker)
	if err == nil || !strings.HasPrefix(err.Error(), "refusing to start:") {
		t.Fatalf("preflightCleanup = %v, want a refusal to start", err)
	}
}

func TestPreflightCleanupWipesPreviousSessionState(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(stateDir(dir), "passdb.tdb")
	if err := os.MkdirAll(stateDir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preflightCleanup(dir); err != nil {
		t.Fatalf("preflightCleanup: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale passdb survived preflight: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir(dir), ".cleanup-probe")); !os.IsNotExist(err) {
		t.Fatal("cleanup probe file was left behind")
	}
}

func TestCleanupRemovesStateAndConfAndToleratesMissing(t *testing.T) {
	dir := t.TempDir()
	if err := cleanup(dir); err != nil {
		t.Fatalf("cleanup of an empty dir = %v, want nil", err)
	}
	if err := os.MkdirAll(stateDir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfPath(dir), []byte("[global]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(dir); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, p := range []string{stateDir(dir), ConfPath(dir)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived cleanup: %v", p, err)
		}
	}
}

func TestCleanupReportsUnremovableConf(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory at the smb.conf path cannot be os.Remove'd.
	conf := ConfPath(dir)
	if err := os.MkdirAll(filepath.Join(conf, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	err := cleanup(dir)
	if err == nil || !strings.Contains(err.Error(), "generated smb.conf") {
		t.Fatalf("cleanup = %v, want smb.conf failure reported", err)
	}
}
