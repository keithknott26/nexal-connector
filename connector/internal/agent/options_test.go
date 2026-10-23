package agent

import (
	"context"
	"testing"

	"nexal/connector/internal/bandwidth"
	"nexal/connector/internal/client"
	"nexal/connector/internal/presence"
	"nexal/connector/internal/sandbox"
)

type stubPresence struct{}

func (stubPresence) Run(context.Context) error   { return nil }
func (stubPresence) Snapshot() presence.Snapshot { return presence.Snapshot{} }

type stubWake struct{}

func (stubWake) ReportWakeInfo(context.Context, client.WakeInfo) error { return nil }

// Each Option must install exactly its own dependency; a nil mesh provider must
// leave the existing one in place so a caller cannot silently clear it.
func TestOptionsInstallDependencies(t *testing.T) {
	a := &Agent{}
	m := &sandbox.Manager{}
	r := &bandwidth.Runner{}
	for _, o := range []Option{
		WithMeteredSource(stubMetered{}),
		WithSandbox(m),
		WithBandwidth(r),
		WithPresence(stubPresence{}),
		WithWakeInfo(stubWake{}),
	} {
		o(a)
	}
	if a.metered == nil || a.sandbox != m || a.bandwidthRunner != r || a.presence == nil || a.wakeReporter == nil {
		t.Fatalf("options did not install dependencies: %+v", a)
	}

	prov := &snapshotProvider{}
	WithMeshProvider(prov)(a)
	WithMeshProvider(nil)(a)
	if a.meshProvider != prov {
		t.Fatal("nil mesh provider replaced the installed one")
	}
}
