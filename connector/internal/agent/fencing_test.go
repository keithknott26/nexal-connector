package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"nexal/connector/internal/client"
)

func TestConsentChangeFencesInFlightProbe(t *testing.T) {
	for _, change := range []string{"policy", "pause-resume"} {
		t.Run(change, func(t *testing.T) {
			a, _ := testAgent(t)
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			probe := a.probe
			a.probe = func(ctx context.Context) Telemetry {
				close(started)
				<-release
				return probe(ctx)
			}
			go func() { a.Refresh(context.Background()); close(done) }()
			<-started
			if change == "policy" {
				p := a.Snapshot().ResourcePolicy
				p.IdleSeconds++
				if err := a.SetResourcePolicy(p); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := a.SetPaused(true); err != nil {
					t.Fatal(err)
				}
				if err := a.SetPaused(false); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			<-done
			if s := a.Snapshot(); s.Telemetry.Known || !s.Telemetry.OwnerActive || s.CoordinatorHealthy {
				t.Fatal("old observation restored consent or coordinator freshness")
			}
			a.probe = probe
			a.Refresh(context.Background())
			if !a.Snapshot().Telemetry.Known {
				t.Fatal("fresh post-policy observation was rejected")
			}
		})
	}
}

func TestOutOfOrderProbeCannotOverwriteNewerOwnerActivity(t *testing.T) {
	a, _ := testAgent(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	a.probe = func(context.Context) Telemetry {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return Telemetry{Known: true, IdleSeconds: 1000, TotalMemoryBytes: 2 << 30, AvailableMemoryBytes: 1 << 30}
		}
		return Telemetry{Known: true, OwnerActive: true}
	}
	go func() { a.Refresh(context.Background()); close(done) }()
	<-started
	a.Refresh(context.Background())
	close(release)
	<-done
	if !a.Snapshot().Telemetry.OwnerActive {
		t.Fatal("older idle sample overwrote newer active-owner sample")
	}
}

type heartbeatAPI struct {
	fakeAPI
	send func(context.Context, client.Heartbeat) error
}

func (f *heartbeatAPI) Heartbeat(ctx context.Context, _ string, h client.Heartbeat) (client.HeartbeatResponse, error) {
	return client.HeartbeatResponse{}, f.send(ctx, h)
}

func TestConsentChangeFencesInFlightHeartbeat(t *testing.T) {
	a, _ := testAgent(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	a.api = &heartbeatAPI{send: func(context.Context, client.Heartbeat) error {
		close(started)
		<-release
		return nil
	}}
	go func() { done <- a.hostHeartbeat(context.Background()) }()
	<-started
	p := a.Snapshot().ResourcePolicy
	p.IdleSeconds++
	if err := a.SetResourcePolicy(p); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().CoordinatorHealthy {
		t.Fatal("old-policy heartbeat restored coordinator freshness")
	}
}

func TestOutOfOrderHeartbeatCannotOverwriteCurrentOutcome(t *testing.T) {
	for _, oldFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-success", true: "old-failure"}[oldFails], func(t *testing.T) {
			a, _ := testAgent(t)
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var calls atomic.Int32
			a.api = &heartbeatAPI{send: func(context.Context, client.Heartbeat) error {
				if calls.Add(1) == 1 {
					close(started)
					<-release
					if oldFails {
						return errors.New("old failure")
					}
					return nil
				}
				if !oldFails {
					return errors.New("new failure")
				}
				return nil
			}}
			go func() { done <- a.hostHeartbeat(context.Background()) }()
			<-started
			_ = a.hostHeartbeat(context.Background())
			close(release)
			<-done
			if a.Snapshot().CoordinatorHealthy != oldFails {
				t.Fatal("old heartbeat overwrote current outcome")
			}
		})
	}
}

func TestHeartbeatDoesNotAdvertiseStaleTelemetry(t *testing.T) {
	a, _ := testAgent(t)
	a.telemetryAt = time.Now().Add(-time.Minute)
	a.api = &heartbeatAPI{send: func(_ context.Context, h client.Heartbeat) error {
		if !h.OwnerActive || h.AvailableMemoryBytes != 0 {
			t.Fatal("stale telemetry advertised compute availability")
		}
		return nil
	}}
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledObservationsCannotRestoreFreshness(t *testing.T) {
	a, _ := testAgent(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := a.Snapshot().ResourcePolicy
	p.IdleSeconds++
	if err := a.SetResourcePolicy(p); err != nil {
		t.Fatal(err)
	}
	a.Refresh(ctx)
	if a.Snapshot().Telemetry.Known {
		t.Fatal("cancelled probe accepted")
	}
	if a.hostHeartbeat(ctx) == nil || a.Snapshot().CoordinatorHealthy {
		t.Fatal("cancelled heartbeat accepted")
	}
}
