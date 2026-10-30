package agent

import (
	"context"
	"errors"
	"testing"

	"nexal/connector/internal/client"
	"nexal/connector/internal/mesh"
)

// tunnelAPI answers heartbeats with a configurable ack and records the
// separate fallback ReportTunnelStatus calls.
type tunnelAPI struct {
	fakeAPI
	acked     bool
	reportErr error
	embedded  *client.TunnelStatusReport
	reports   int
}

func (f *tunnelAPI) Heartbeat(_ context.Context, _ string, h client.Heartbeat) (client.HeartbeatResponse, error) {
	f.embedded = h.TunnelStatus
	return client.HeartbeatResponse{TunnelStatusAcked: f.acked}, nil
}

func (f *tunnelAPI) ReportTunnelStatus(context.Context, string, mesh.Status) error {
	f.reports++
	return f.reportErr
}

func enrolledTunnelAgent(t *testing.T, api *tunnelAPI) *Agent {
	t.Helper()
	a, _ := testAgent(t)
	a.api = api
	a.meshProvider = &gatedProvider{snapshotProvider{mesh.Status{Lifecycle: mesh.LifecycleConnected}}, true}
	return a
}

func TestHeartbeatEmbedsTunnelReportAndRecordsBatchedAck(t *testing.T) {
	api := &tunnelAPI{acked: true}
	a := enrolledTunnelAgent(t, api)
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.embedded == nil || api.reports != 0 {
		t.Fatalf("embedded=%v separate reports=%d, want embedded report and no fallback", api.embedded, api.reports)
	}
	a.mu.Lock()
	reported, lifecycle := !a.lastTunnelReport.IsZero(), a.lastTunnelLifecycle
	a.mu.Unlock()
	if !reported || lifecycle == "" {
		t.Fatalf("acked report not recorded: reported=%v lifecycle=%q", reported, lifecycle)
	}
	// Unchanged lifecycle inside the report interval: nothing is embedded again.
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.embedded != nil {
		t.Fatal("tunnel report re-embedded although nothing changed")
	}
}

func TestHeartbeatFallsBackToSeparateTunnelReportWhenNotAcked(t *testing.T) {
	api := &tunnelAPI{}
	a := enrolledTunnelAgent(t, api)
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.reports != 1 {
		t.Fatalf("separate reports=%d, want 1", api.reports)
	}
	a.mu.Lock()
	reported := !a.lastTunnelReport.IsZero()
	a.mu.Unlock()
	if !reported {
		t.Fatal("successful fallback report was not recorded")
	}
}

func TestRejectedTunnelReportIsNotRecordedAndStillRetried(t *testing.T) {
	api := &tunnelAPI{reportErr: errors.New("coordinator said no")}
	a := enrolledTunnelAgent(t, api)
	for i := 1; i <= 2; i++ {
		if err := a.hostHeartbeat(context.Background()); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
		if api.reports != i {
			t.Fatalf("after heartbeat %d separate reports=%d, want %d", i, api.reports, i)
		}
	}
	a.mu.Lock()
	reported, warned := !a.lastTunnelReport.IsZero(), !a.lastTunnelWarn.IsZero()
	a.mu.Unlock()
	if reported || !warned {
		t.Fatalf("reported=%v warned=%v, want a failed report left unrecorded and a warning stamped", reported, warned)
	}
}
