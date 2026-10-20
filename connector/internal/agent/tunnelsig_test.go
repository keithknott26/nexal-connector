package agent

import (
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/mesh"
)

func TestTunnelSignatureIgnoresPeerOrderAndTraffic(t *testing.T) {
	now := time.Now()
	a := mesh.Peer{ID: "a", Lifecycle: mesh.LifecycleConnected, Path: "direct", LatencyMS: 5}
	b := mesh.Peer{ID: "b", Lifecycle: mesh.LifecycleConnected, Path: "relay"}
	first := tunnelSignature(mesh.Status{Peers: []mesh.Peer{a, b}}, now)
	a.LatencyMS = 90
	second := tunnelSignature(mesh.Status{Peers: []mesh.Peer{b, a}}, now)
	if first != second {
		t.Fatalf("signature should ignore order and latency: %q vs %q", first, second)
	}
}

func TestTunnelSignatureChangesWithPeerPathAndLifecycle(t *testing.T) {
	now := time.Now()
	base := mesh.Status{Lifecycle: mesh.LifecycleConnected, Peers: []mesh.Peer{{ID: "a", Lifecycle: mesh.LifecycleConnected, Path: "direct"}}}
	want := tunnelSignature(base, now)
	if !strings.Contains(want, "a:") {
		t.Fatalf("signature missing peer id: %q", want)
	}
	moved := base
	moved.Peers = []mesh.Peer{{ID: "a", Lifecycle: mesh.LifecycleConnected, Path: "relay"}}
	if tunnelSignature(moved, now) == want {
		t.Fatal("path change must change the signature")
	}
	down := base
	down.Lifecycle = mesh.Lifecycle("disconnected")
	if tunnelSignature(down, now) == want {
		t.Fatal("lifecycle change must change the signature")
	}
}

func TestPeerRowDisplayNameFallsBackToHostID(t *testing.T) {
	if got := (PeerRow{HostID: "h1"}).displayName(); got != "h1" {
		t.Fatalf("got %q", got)
	}
	if got := (PeerRow{HostID: "h1", Name: "Studio"}).displayName(); got != "Studio" {
		t.Fatalf("got %q", got)
	}
}

type snapshotProvider struct{ status mesh.Status }

func (p *snapshotProvider) Snapshot() mesh.Status { return p.status }

type gatedProvider struct {
	snapshotProvider
	enrolled bool
}

func (p *gatedProvider) Enrolled() bool { return p.enrolled }

func TestTunnelChangedGates(t *testing.T) {
	connected := mesh.Status{Lifecycle: mesh.LifecycleConnected}
	sig := func(s mesh.Status) string {
		return tunnelSignature(mesh.SanitizeSnapshot(s), time.Now())
	}
	enrolledCfg := func(a *Agent) { a.cfg.Enrollment = &config.EnrollmentState{} }

	t.Run("no provider", func(t *testing.T) {
		a := &Agent{lastTunnelLifecycle: "x"}
		if a.tunnelChanged() {
			t.Fatal("nil provider must report no change")
		}
	})
	t.Run("not enrolled", func(t *testing.T) {
		a := &Agent{meshProvider: &snapshotProvider{connected}, lastTunnelLifecycle: "x"}
		if a.tunnelChanged() {
			t.Fatal("unenrolled agent must report no change")
		}
	})
	t.Run("nothing sent yet", func(t *testing.T) {
		a := &Agent{meshProvider: &snapshotProvider{connected}}
		enrolledCfg(a)
		if a.tunnelChanged() {
			t.Fatal("empty last signature must report no change")
		}
	})
	t.Run("same signature", func(t *testing.T) {
		a := &Agent{meshProvider: &snapshotProvider{connected}, lastTunnelLifecycle: sig(connected)}
		enrolledCfg(a)
		if a.tunnelChanged() {
			t.Fatal("unchanged status must report no change")
		}
	})
	t.Run("different signature", func(t *testing.T) {
		a := &Agent{meshProvider: &snapshotProvider{connected}, lastTunnelLifecycle: "stale"}
		enrolledCfg(a)
		if !a.tunnelChanged() {
			t.Fatal("changed status must report change")
		}
	})
	t.Run("provider gate overrides config", func(t *testing.T) {
		a := &Agent{meshProvider: &gatedProvider{snapshotProvider{connected}, false}, lastTunnelLifecycle: "stale"}
		enrolledCfg(a)
		if a.tunnelChanged() {
			t.Fatal("provider reporting not enrolled must win over config")
		}
		a.meshProvider = &gatedProvider{snapshotProvider{connected}, true}
		a.cfg.Enrollment = nil
		if !a.tunnelChanged() {
			t.Fatal("provider reporting enrolled must win over config")
		}
	})
}
