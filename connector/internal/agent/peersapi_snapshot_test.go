package agent

import (
	"net/netip"
	"testing"

	"nexal/connector/internal/discovery"
	"nexal/connector/internal/mesh"
)

type staticMeshProvider struct{ status mesh.Status }

func (p staticMeshProvider) Snapshot() mesh.Status { return p.status }

// Without a mesh provider the snapshot reports LAN peers only, states that no
// private route exists, and describes an unroutable or address-less peer
// without ever offering an address.
func TestPeersSnapshotWithoutMeshExplainsMissingRoutes(t *testing.T) {
	a := &Agent{meshProvider: mesh.UnavailableProvider{}}
	a.peerView = PeerView{
		Directory:          discovery.Snapshot{Stale: true},
		LastAdvertiseError: "coordinator unreachable",
		Peers: []discovery.Peer{
			{HostID: "h-b", Name: "b-mac"},
			{HostID: "h-a", Name: "a-mac", Addresses: []netip.AddrPort{mustAddrPort(t, "8.8.8.8:22")}},
		},
	}

	got := a.PeersSnapshot()
	if got.RemoteAccessAvailable || !got.DirectoryStale || got.LastError != "coordinator unreachable" {
		t.Fatalf("snapshot flags = %+v", got)
	}
	if len(got.Peers) != 2 {
		t.Fatalf("peers = %+v", got.Peers)
	}
	// Neither is same-network, so they sort by display name.
	if got.Peers[0].Name != "a-mac" || got.Peers[0].Reachability != "no-route" || got.Peers[0].Address != "" {
		t.Fatalf("a-mac row = %+v", got.Peers[0])
	}
	if got.Peers[1].Name != "b-mac" || got.Peers[1].Reachability != "unknown" || got.Peers[1].Note == "" {
		t.Fatalf("b-mac row = %+v", got.Peers[1])
	}
}

// A mesh provider that is available marks remote access available and lists
// its connected peers ahead of peers with no route.
func TestPeersSnapshotMergesMeshPeersFirst(t *testing.T) {
	a := &Agent{meshProvider: staticMeshProvider{mesh.Status{
		ProviderAvailable: true,
		Peers: []mesh.Peer{{ID: "k1", Name: "z-mesh", Lifecycle: mesh.LifecycleConnected,
			PathLabel: "Direct", TunnelAddress: "100.64.0.9"}},
	}}}
	a.peerView = PeerView{Peers: []discovery.Peer{{HostID: "h-a", Name: "a-mac"}}}

	got := a.PeersSnapshot()
	if !got.RemoteAccessAvailable {
		t.Fatalf("RemoteAccessAvailable = false, want true")
	}
	if len(got.Peers) != 2 || got.Peers[0].HostID != "mesh:k1" || got.Peers[0].Address != "100.64.0.9" || got.Peers[1].HostID != "h-a" {
		t.Fatalf("peers = %+v", got.Peers)
	}
}
