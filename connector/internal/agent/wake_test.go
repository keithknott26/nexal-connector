package agent

import (
	"testing"
	"time"

	"nexal/connector/internal/mesh"
)

// The Wake* nudges are called from the presence relay goroutine; they must
// never block even when nobody is draining the channel, and repeated nudges
// must coalesce into one pending wake.
func TestWakeNudgesAreNonBlockingAndCoalesce(t *testing.T) {
	a, _ := testAgent(t)
	for name, tc := range map[string]struct {
		wake func()
		ch   chan struct{}
	}{
		"selftest":  {a.WakeSelfTest, a.selfTestWake},
		"heartbeat": {a.WakeHeartbeat, a.heartbeatWake},
	} {
		for len(tc.ch) > 0 {
			<-tc.ch
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			tc.wake()
			tc.wake()
			tc.wake()
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: wake blocked with no receiver", name)
		}
		if got := len(tc.ch); got != 1 {
			t.Fatalf("%s: pending wakes = %d, want 1", name, got)
		}
	}
}

func TestWithMeshProviderIgnoresNil(t *testing.T) {
	a, _ := testAgent(t)
	before := a.meshProvider
	WithMeshProvider(nil)(a)
	if a.meshProvider != before {
		t.Fatal("nil provider replaced the existing mesh provider")
	}
	WithMeshProvider(mesh.UnavailableProvider{})(a)
	if a.meshProvider == nil {
		t.Fatal("provider not installed")
	}
}

// wakeStatusLocked is what the local API serializes: MACs must be a non-nil
// slice, the network setting must default to "unknown", and the returned MACs
// must be a copy so callers cannot mutate the agent's own record.
func TestWakeStatusLockedDefaultsAndCopiesMACs(t *testing.T) {
	a, _ := testAgent(t)
	a.mu.Lock()
	a.wake = WakeStatus{}
	got := a.wakeStatusLocked()
	a.mu.Unlock()
	if got.MACs == nil || len(got.MACs) != 0 || got.WakeForNetwork != "unknown" {
		t.Fatalf("empty wake status = %+v", got)
	}

	a.mu.Lock()
	a.wake = WakeStatus{MACs: []string{"3c:22:fb:01:02:03"}, WakeForNetwork: "enabled", Reported: true}
	got = a.wakeStatusLocked()
	got.MACs[0] = "mutated"
	again := a.wakeStatusLocked()
	a.mu.Unlock()
	if again.MACs[0] != "3c:22:fb:01:02:03" || again.WakeForNetwork != "enabled" || !again.Reported {
		t.Fatalf("wake status aliased or altered: %+v", again)
	}
}
