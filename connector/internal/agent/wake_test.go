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
