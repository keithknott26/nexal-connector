package mesh

import (
	"testing"
	"time"
)

func TestUnavailableProviderFailsClosed(t *testing.T) {
	s := (UnavailableProvider{}).Snapshot()
	if s.ProviderAvailable || s.Lifecycle != LifecycleUnavailable || s.PQ != PQUnsupported || s.Peers == nil {
		t.Fatalf("unexpected unavailable status: %#v", s)
	}
	if s.Discovery.Gateway != "" || s.Discovery.Bridge != "" {
		t.Fatalf("unavailable provider fabricated discovery: %#v", s.Discovery)
	}
}

func TestSanitizeSnapshotRejectsUpstreamAndUnauthorizedSharing(t *testing.T) {
	s := Status{Peers: []Peer{{Hostname: HostnameStatus{State: "ready", Hostname: "device.netbird.cloud"},
		FileSharing:   FileSharing{Authorized: true, Available: true, Address: "device.netbird.cloud"},
		ScreenSharing: ScreenSharing{Authorized: false, Available: true, Address: "device.netbird.cloud"}}}}
	got := SanitizeSnapshot(s).Peers[0]
	if got.Hostname.Hostname != "" || got.FileSharing.Available || got.FileSharing.Address != "" || got.ScreenSharing.Available {
		t.Fatalf("unsafe provider data survived: %#v", got)
	}

	host := "studio.network.mesh.nexal.systems"
	s.Peers[0] = Peer{Hostname: HostnameStatus{State: "ready", Hostname: host}, PQ: PQProtected, PQVerifiedAt: time.Now().UTC().Format(time.RFC3339Nano),
		FileSharing:   FileSharing{Authorized: true, Available: true, Address: host},
		ScreenSharing: ScreenSharing{Authorized: true, Available: true, Address: host}}
	got = SanitizeSnapshot(s).Peers[0]
	if !got.FileSharing.Available || !got.ScreenSharing.Available {
		t.Fatalf("safe sharing removed: %#v", got)
	}
}

func TestStrictPQReadyRequiresRuntimePeerEvidence(t *testing.T) {
	now := time.Now().UTC()
	s := Status{PQ: PQProtected, Peers: []Peer{{Lifecycle: LifecycleConnected, PQ: PQProtected, PQVerifiedAt: now.Format(time.RFC3339Nano)}}}
	if !s.StrictPQReadyAt(now, 2*time.Minute) {
		t.Fatal("valid peer evidence rejected")
	}
	s.Peers[0].PQVerifiedAt = ""
	if s.StrictPQReadyAt(now, 2*time.Minute) {
		t.Fatal("missing runtime verification accepted")
	}
	s.Peers[0].PQVerifiedAt = now.Add(-3 * time.Minute).Format(time.RFC3339Nano)
	if s.StrictPQReadyAt(now, 2*time.Minute) {
		t.Fatal("stale verification accepted")
	}
}
