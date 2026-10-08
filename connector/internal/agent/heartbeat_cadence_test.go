package agent

import (
	"context"
	"nexal/connector/internal/client"
	"testing"
	"time"
)

func TestHeartbeatCadencePreservesRunnableWork(t *testing.T) {
	a, _ := testAgent(t)
	a.devPull = true
	if got := a.hostHeartbeatIntervalLocked(); got != 15*time.Second {
		t.Fatalf("runnable cadence %v", got)
	}
	a.cfg.Paused = true
	if got := a.hostHeartbeatIntervalLocked(); got != 5*time.Minute {
		t.Fatalf("paused cadence %v", got)
	}
	a.active = "attempt"
	if got := a.hostHeartbeatIntervalLocked(); got != 15*time.Second {
		t.Fatalf("active lease cadence %v", got)
	}
	a.active = ""
	a.cfg.Paused = false
	a.cfg.Development = false
	if got := a.hostHeartbeatIntervalLocked(); got != 5*time.Minute {
		t.Fatalf("production gated cadence %v", got)
	}
	a.lastHeartbeat = time.Now().Add(-time.Minute)
	if !a.Snapshot().CoordinatorHealthy {
		t.Fatal("background check incorrectly reported unhealthy")
	}
	a.cfg.Development = true
	if a.Snapshot().CoordinatorHealthy {
		t.Fatal("stale runnable scheduler heartbeat marked healthy")
	}
}

type cadenceAPI struct {
	fakeAPI
	calls int
}

func (c *cadenceAPI) Heartbeat(context.Context, string, client.Heartbeat) (client.HeartbeatResponse, error) {
	c.calls++
	return client.HeartbeatResponse{}, nil
}
func TestBackgroundHeartbeatDefersOnlyPeriodicRequests(t *testing.T) {
	a, _ := testAgent(t)
	api := &cadenceAPI{}
	a.api = api
	a.cfg.Paused = true
	a.lastHeartbeat = time.Now()
	a.heartbeat(context.Background(), "interval")
	if api.calls != 0 {
		t.Fatal("background interval sent early")
	}
	a.heartbeat(context.Background(), "consent-change")
	if api.calls != 1 {
		t.Fatal("explicit transition did not refresh immediately")
	}
	a.lastHeartbeat = time.Now().Add(-5 * time.Minute)
	a.heartbeat(context.Background(), "interval")
	if api.calls != 2 {
		t.Fatal("background freshness refresh missing")
	}
	a.cfg.Paused = false
	a.devPull = true
	a.heartbeat(context.Background(), "interval")
	if api.calls != 3 {
		t.Fatal("runnable heartbeat was deferred")
	}
}
