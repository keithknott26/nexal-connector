package client

import (
	"testing"

	"nexal/connector/internal/mesh"
)

func TestBestRuntimePeerSelection(t *testing.T) {
	if got := bestRuntimePeer(nil); got != nil {
		t.Fatalf("empty peers = %+v, want nil", got)
	}
	if got := bestRuntimePeer([]mesh.Peer{{ID: "a", Lifecycle: mesh.LifecycleProvisioning, LatencyMS: 1}}); got != nil {
		t.Fatalf("non-usable peer selected: %+v", got)
	}

	cases := []struct {
		name  string
		peers []mesh.Peer
		want  string
	}{
		{"lowest latency wins", []mesh.Peer{
			{ID: "slow", Lifecycle: mesh.LifecycleConnected, LatencyMS: 80},
			{ID: "fast", Lifecycle: mesh.LifecycleConnected, LatencyMS: 20},
		}, "fast"},
		{"degraded is eligible", []mesh.Peer{
			{ID: "down", Lifecycle: mesh.LifecycleUnavailable, LatencyMS: 1},
			{ID: "deg", Lifecycle: mesh.LifecycleDegraded, LatencyMS: 50},
		}, "deg"},
		{"measured latency beats unmeasured", []mesh.Peer{
			{ID: "unmeasured", Lifecycle: mesh.LifecycleConnected},
			{ID: "measured", Lifecycle: mesh.LifecycleConnected, LatencyMS: 40},
		}, "measured"},
		{"first usable kept when none measured", []mesh.Peer{
			{ID: "first", Lifecycle: mesh.LifecycleConnected},
			{ID: "second", Lifecycle: mesh.LifecycleConnected},
		}, "first"},
		{"unmeasured later peer does not displace measured", []mesh.Peer{
			{ID: "measured", Lifecycle: mesh.LifecycleConnected, LatencyMS: 40},
			{ID: "unmeasured", Lifecycle: mesh.LifecycleConnected},
		}, "measured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bestRuntimePeer(tc.peers)
			if got == nil || got.ID != tc.want {
				t.Fatalf("bestRuntimePeer = %+v, want %s", got, tc.want)
			}
		})
	}
}
