package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"nexal/connector/internal/client"
)

type countingSelfTestAPI struct {
	*fakeAPI
	polls atomic.Int32
}

func (c *countingSelfTestAPI) SelfTest(context.Context, string) (bool, *client.Attempt, error) {
	c.polls.Add(1)
	return false, nil, nil
}

// runSelfTest must not poll the coordinator while an attempt is active, must
// poll promptly once nudged while idle, and must exit when its context ends.
func TestRunSelfTestSkipsPollWhileBusyAndStopsOnCancel(t *testing.T) {
	a, f := testAgent(t)
	api := &countingSelfTestAPI{fakeAPI: f}
	a.api = api

	a.mu.Lock()
	a.active = "attempt-1"
	a.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runSelfTest(ctx, "h1")
	}()

	a.WakeSelfTest()
	time.Sleep(100 * time.Millisecond)
	if got := api.polls.Load(); got != 0 {
		t.Fatalf("polled %d times while busy, want 0", got)
	}

	a.mu.Lock()
	a.active = ""
	a.mu.Unlock()
	a.WakeSelfTest()
	deadline := time.Now().Add(2 * time.Second)
	for api.polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := api.polls.Load(); got != 1 {
		t.Fatalf("polls after idle wake = %d, want 1", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSelfTest did not return after context cancel")
	}
}
