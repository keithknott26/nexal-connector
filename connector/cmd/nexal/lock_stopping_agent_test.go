package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

// With nothing holding the configuration lock, lockStoppingAgent just takes it.
func TestLockStoppingAgentUncontended(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	unlock, err := lockStoppingAgent(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// While held, a second taker must be refused; after unlock it must succeed.
	if _, err := config.Lock(path); err == nil {
		t.Fatal("lock was not held")
	}
	unlock()
	again, err := config.Lock(path)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	again()
}

// A lock held by something that is not a registered running agent is reported,
// never signalled or waited on.
func TestLockStoppingAgentLeavesUnknownHolderAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no machine lock file: RunningAgent reports nobody
	path := filepath.Join(t.TempDir(), "config.json")
	held, err := config.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	unlock, err := lockStoppingAgent(context.Background(), path)
	if err == nil {
		unlock()
		t.Fatal("expected the lock error")
	}
	if !strings.Contains(err.Error(), "another connector process holds this configuration") {
		t.Fatalf("unexpected error: %v", err)
	}
}
