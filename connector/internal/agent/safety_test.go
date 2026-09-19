package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"nexal/connector/internal/client"
)

func TestObservationExpiryReclaimsRunningWork(t *testing.T) {
	for _, stale := range []string{"telemetry", "coordinator"} {
		t.Run(stale, func(t *testing.T) {
			a, f := testAgent(t)
			at := attempt()
			at.Samples = MaxSamples
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			done := make(chan error, 1)
			go func() { done <- a.Execute(ctx, at) }()
			deadline := time.Now().Add(2 * time.Second)
			for a.Snapshot().ActiveAttempt == "" && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if a.Snapshot().ActiveAttempt == "" {
				t.Fatal("work did not start")
			}
			// Model a stalled telemetry or heartbeat loop: no Refresh or
			// network response is available to perform cancellation itself.
			a.mu.Lock()
			if stale == "telemetry" {
				a.telemetryAt = time.Now().Add(-time.Minute)
			} else {
				a.lastHeartbeat = time.Now().Add(-time.Minute)
			}
			a.mu.Unlock()
			select {
			case err := <-done:
				if err == nil || f.count() != 0 {
					t.Fatal("expired observations permitted completion")
				}
			case <-time.After(time.Second):
				t.Fatal("stalled monitor prevented prompt reclaim")
			}
		})
	}
}

func TestLateProbeDoesNotRefreshOldObservations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _ := testAgent(t)
		probe := a.probe
		a.probe = func(ctx context.Context) Telemetry {
			time.Sleep(11 * time.Second)
			return probe(ctx)
		}
		a.Refresh(context.Background())
		a.mu.Lock()
		err := a.admitLocked(false)
		a.mu.Unlock()
		if err == nil {
			t.Fatal("delayed telemetry became fresh on completion")
		}
		a.api = &heartbeatAPI{send: func(_ context.Context, h client.Heartbeat) error {
			if !h.OwnerActive || h.AvailableMemoryBytes != 0 {
				t.Error("old observation advertised availability")
			}
			return nil
		}}
		_ = a.hostHeartbeat(context.Background())
	})
}

func TestConsentChangeFencesPendingPullEvenAfterFreshObservations(t *testing.T) {
	for _, change := range []string{"cancel", "pause-resume", "policy"} {
		t.Run(change, func(t *testing.T) {
			a, f := testAgent(t)
			generation := a.stateGeneration
			switch change {
			case "cancel":
				a.Cancel()
			case "pause-resume":
				if err := a.SetPaused(true); err != nil {
					t.Fatal(err)
				}
				if err := a.SetPaused(false); err != nil {
					t.Fatal(err)
				}
			case "policy":
				p := a.Snapshot().ResourcePolicy
				p.IdleSeconds++
				if err := a.SetResourcePolicy(p); err != nil {
					t.Fatal(err)
				}
			}
			a.Refresh(context.Background())
			if err := a.hostHeartbeat(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := a.execute(context.Background(), attempt(), &generation); err == nil || f.count() != 0 {
				t.Fatal("old pull admitted under a new consent generation")
			}
			if err := a.Execute(context.Background(), attempt()); err != nil {
				t.Fatalf("fresh admission should still work: %v", err)
			}
		})
	}
}

func TestCancelledExecutionDoesNotConsumeAttempt(t *testing.T) {
	a, f := testAgent(t)
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if !errors.Is(a.Execute(ctx, attempt()), context.Canceled) {
		t.Fatal("cancelled execution was admitted")
	}
	if len(a.records) != 0 || f.count() != 0 {
		t.Fatal("cancelled caller consumed the attempt journal")
	}
}

func TestAgentRequestsBoundedByLeaseAndNetworkDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, lease := range []time.Duration{0, time.Second, time.Minute} {
			expiry := time.Time{}
			want := 10 * time.Second
			if lease != 0 {
				expiry = time.Now().Add(lease)
				if lease < want {
					want = lease
				}
			}
			ctx, stop := boundedRequest(context.Background(), expiry)
			start := time.Now()
			<-ctx.Done()
			stop()
			if time.Since(start) != want {
				t.Fatal("request timeout did not honor tighter bound")
			}
		}
		a, _ := testAgent(t)
		a.api = &heartbeatAPI{send: func(ctx context.Context, _ client.Heartbeat) error {
			<-ctx.Done()
			return nil // a late success must not restore freshness
		}}
		start := time.Now()
		if err := a.hostHeartbeat(context.Background()); err == nil ||
			time.Since(start) != 10*time.Second || a.Snapshot().CoordinatorHealthy {
			t.Fatal("timed out heartbeat restored freshness")
		}
	})
}

func TestMemoryTelemetryRejectsDuplicateCounters(t *testing.T) {
	base := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 100.\nPages speculative: 100.\n"
	for _, extra := range []string{"Pages free: 0.\n", "Pages speculative: 0.\n", "page size of 4096 bytes\n"} {
		if _, err := ParseFreeMemory([]byte(base + extra)); err == nil {
			t.Fatal("ambiguous memory reading accepted")
		}
	}
}
