package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

// runSandbox starts the power and kill-request goroutines and must join them
// and return promptly once its context ends, without ever polling.
func TestRunSandboxReturnsWhenContextIsCancelled(t *testing.T) {
	a, _ := testAgent(t)
	dir := t.TempDir()
	m, err := sandbox.NewManager(sandbox.Options{
		DataDir:    dir,
		Hypervisor: struct{ sandbox.Hypervisor }{},
		Images:     sandbox.NewImageStore(filepath.Join(dir, "images"), nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	a.sandbox = m
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runSandbox(ctx, "h1")
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runSandbox did not return after its context was cancelled")
	}
}
