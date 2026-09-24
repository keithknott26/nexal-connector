package agent

import (
	"testing"

	"nexal/connector/internal/mesh"
)

func TestMeshPeerRowConnectedIsReachableAtTunnelAddress(t *testing.T) {
	row := meshPeerRow(mesh.Peer{ID: "k1", Name: "keiths-macbook-pro", Lifecycle: mesh.LifecycleDegraded,
		PathLabel: "Direct", TunnelAddress: "100.113.99.69"})
	if row.Reachability != "same-network" || row.Address != "100.113.99.69" || row.HostID != "mesh:k1" || !row.Authorized {
		t.Fatalf("row = %+v", row)
	}
}

func TestMeshPeerRowIdleHasNoAddress(t *testing.T) {
	row := meshPeerRow(mesh.Peer{ID: "k1", Name: "m2", Lifecycle: mesh.LifecycleUnavailable, TunnelAddress: "100.113.174.101"})
	if row.Reachability != "unknown" || row.Address != "" {
		t.Fatalf("row = %+v", row)
	}
}
