//go:build darwin || linux

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockReleasedByUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	again, err := Lock(path)
	if err != nil {
		t.Fatalf("lock not reacquirable after unlock: %v", err)
	}
	again()
}

func TestLockRefusesNonPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	lock := filepath.Join(filepath.Dir(path), "agent.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lock, 0o644); err != nil {
		t.Fatal(err)
	}
	unlock, err := Lock(path)
	if err == nil {
		unlock()
		t.Fatal("group/world-readable lock file accepted")
	}
	if !strings.Contains(err.Error(), "private") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "agent.lock")); err != nil {
		t.Fatal(err)
	}
	if unlock, err := Lock(filepath.Join(dir, "config.json")); err == nil {
		unlock()
		t.Fatal("symlinked lock file followed")
	}
}
