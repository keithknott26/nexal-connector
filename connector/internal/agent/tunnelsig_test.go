package agent

import (
	"strings"
	"testing"
	"time"

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
