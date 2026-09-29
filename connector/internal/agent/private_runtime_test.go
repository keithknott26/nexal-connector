package agent

import (
	"context"
	"testing"
	"time"
)

func TestPrivateRuntimeExclusiveReservation(t *testing.T) {
	a, _ := testAgent(t)
	ctx, release, err := a.ReservePrivateRuntime(context.Background(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, err := a.ReservePrivateRuntime(context.Background(), 64<<20); err == nil {
		t.Fatal("double reservation")
	}
	if err := a.Execute(context.Background(), attempt()); err == nil {
		t.Fatal("overlapping compute")
	}
	if ctx.Err() != nil {
		t.Fatal("new reservation cancelled")
	}
	release()
	release()
	if err := a.Execute(context.Background(), attempt()); err != nil {
		t.Fatal(err)
	}
}
func TestPrivateRuntimeRejectsUnapprovedMemory(t *testing.T) {
	for _, memory := range []uint64{0, 257 << 20, ^uint64(0)} {
		a, _ := testAgent(t)
		if _, _, err := a.ReservePrivateRuntime(context.Background(), memory); err == nil {
			t.Fatalf("accepted %d", memory)
		}
		if a.runtimeMemory != 0 || a.cancel != nil {
			t.Fatal("failed admission leaked slot")
		}
	}
	a, _ := testAgent(t)
	a.telemetry.AvailableMemoryBytes = a.cfg.ReserveMemoryBytes + (64 << 20)
	if _, _, err := a.ReservePrivateRuntime(context.Background(), 128<<20); err == nil {
		t.Fatal("ignored free memory")
	}
}
func TestPrivateRuntimeRevocationRetainsSlotUntilCleanup(t *testing.T) {
	for _, cause := range []string{"pause", "policy", "stale", "owner", "pressure", "parent"} {
		t.Run(cause, func(t *testing.T) {
			a, _ := testAgent(t)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, release, err := a.ReservePrivateRuntime(parent, 128<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			switch cause {
			case "pause":
				if err := a.SetPaused(true); err != nil {
					t.Fatal(err)
				}
			case "policy":
				p := a.cfg.ResourcePolicy()
				p.MemoryLimitBytes = 64 << 20
				if err := a.SetResourcePolicy(p); err != nil {
					t.Fatal(err)
				}
			case "parent":
				cancel()
			default:
				a.mu.Lock()
				switch cause {
				case "stale":
					a.telemetryAt = time.Now().Add(-time.Minute)
				case "owner":
					a.telemetry.OwnerActive = true
				case "pressure":
					a.telemetry.AvailableMemoryBytes = a.cfg.ReserveMemoryBytes + (100 << 20)
				}
				a.mu.Unlock()
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("runtime not reclaimed")
			}
			a.mu.Lock()
			reserved := a.runtimeMemory
			a.mu.Unlock()
			if reserved == 0 {
				t.Fatal("slot released before cleanup")
			}
			release()
			a.mu.Lock()
			reserved = a.runtimeMemory
			a.mu.Unlock()
			if reserved != 0 {
				t.Fatal("slot leaked")
			}
		})
	}
}
