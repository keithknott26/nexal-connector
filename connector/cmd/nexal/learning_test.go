package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLearningStopsOfflineAndCancelsOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var connected atomic.Bool
	var calls atomic.Int32
	started := make(chan struct{})
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runLearningWithInterval(ctx, connected.Load, func(call context.Context) error {
			calls.Add(1)
			close(started)
			<-call.Done()
			close(stopped)
			return call.Err()
		}, 5*time.Millisecond, time.Millisecond)
	}()
	time.Sleep(15 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("ran while disconnected")
	}
	connected.Store(true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("did not start connected")
	}
	connected.Store(false)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("did not cancel on disconnect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop")
	}
}
