package main

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestExitWithParentIgnoresInvalidPids(t *testing.T) {
	for _, pid := range []string{"", "abc", "0", "1", "-5"} {
		stopped := make(chan struct{}, 1)
		exitWithParent(pid, context.CancelFunc(func() { stopped <- struct{}{} }))
		select {
		case <-stopped:
			t.Fatalf("pid %q must not arm the parent watcher", pid)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestExitWithParentStopsWhenParentIsNotTheNamedPid(t *testing.T) {
	// A pid that is not our parent models "the agent is already gone".
	stopped := make(chan struct{}, 1)
	exitWithParent(strconv.Itoa(os.Getppid()+1000000), context.CancelFunc(func() { stopped <- struct{}{} }))
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop was not called although the named parent is not ours")
	}
}
