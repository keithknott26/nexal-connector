package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nexal/connector/internal/cybersecurity"
)

func TestRunCanaryReportsStateOnceAndStopsOnCancel(t *testing.T) {
	monitor := cybersecurity.CanaryForConfig(filepath.Join(t.TempDir(), "config.json"))
	ctx, cancel := context.WithCancel(context.Background())
	states := make(chan cybersecurity.CanaryState, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCanary(ctx, monitor,
			func(context.Context, cybersecurity.Event) error {
				t.Error("no event expected for a disabled canary")
				return nil
			},
			func(_ context.Context, s cybersecurity.CanaryState) error { states <- s; return nil })
	}()
	select {
	case s := <-states:
		if s.Enabled {
			t.Fatalf("fresh canary should be disabled: %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("state was not reported on the first iteration")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runCanary did not return after context cancel")
	}
	if len(states) != 0 {
		t.Fatalf("expected a single state report, got %d extra", len(states))
	}
}

func TestRunCanaryWithoutStateReporterStopsOnCancel(t *testing.T) {
	monitor := cybersecurity.CanaryForConfig(filepath.Join(t.TempDir(), "config.json"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCanary(ctx, monitor, func(context.Context, cybersecurity.Event) error { return nil })
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runCanary did not return after context cancel")
	}
}
