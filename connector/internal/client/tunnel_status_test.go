package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/mesh"
)

func TestTunnelReportMapsOnlyFreshVerifiedRuntimeEvidence(t *testing.T) {
	now := time.Now().UTC()
	status := mesh.Status{Lifecycle: mesh.LifecycleConnected, PQ: mesh.PQProtected, Peers: []mesh.Peer{{
		ID: "peer", Lifecycle: mesh.LifecycleConnected, Path: mesh.PathRelay, RelayRegion: "New York",
		LatencyMS: 31.4, PacketLossPercent: .2, PQ: mesh.PQProtected, PQVerifiedAt: now.Format(time.RFC3339Nano),
		LastHandshakeAt: now.Add(-time.Second).Format(time.RFC3339Nano),
		Traffic:         mesh.Traffic{SentBytes: 12, ReceivedBytes: 34, LastAt: now.Format(time.RFC3339Nano)},
	}}}
	report := TunnelReportFromRuntime(status, now)
	if report.AuthStage != "connected" || report.SecurityState != "quantum_protected" || report.Path.Type != "nexal_relay" || report.Path.RelayRegion != "New York" || report.PQVerifiedAt == "" {
		t.Fatalf("verified runtime evidence mapped incorrectly: %#v", report)
	}
	if report.Traffic.BytesSent != 12 || report.Traffic.BytesReceived != 34 {
		t.Fatalf("traffic lost: %#v", report.Traffic)
	}
}

func TestTunnelReportCarriesEveryConnectedPeerWithoutInventingCoordinatorTraffic(t *testing.T) {
	now := time.Now().UTC()
	status := mesh.Status{Lifecycle: mesh.LifecycleConnected, Peers: []mesh.Peer{
		{ID: "a", Name: "Studio", Lifecycle: mesh.LifecycleConnected, Path: mesh.PathDirect, LatencyMS: 4.4, Traffic: mesh.Traffic{SentBytes: 10, ReceivedBytes: 20}},
		{ID: "b", Name: "Office", Lifecycle: mesh.LifecycleDegraded, Path: mesh.PathRelay, RelayRegion: "New York", LatencyMS: 21, Traffic: mesh.Traffic{SentBytes: 30, ReceivedBytes: 40}},
		{ID: "offline", Lifecycle: mesh.LifecycleUnavailable},
	}}
	report := TunnelReportFromRuntime(status, now)
	if len(report.Peers) != 2 || report.Peers[0].ID != "a" || report.Peers[1].Traffic.BytesReceived != 40 {
		t.Fatalf("per-peer telemetry = %+v", report.Peers)
	}
	wire, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "controlPlaneTraffic") {
		t.Fatalf("invented coordinator traffic: %s", wire)
	}
}

func TestTunnelReportFailsClosedForStaleOrMissingEvidence(t *testing.T) {
	now := time.Now().UTC()
	status := mesh.Status{Lifecycle: mesh.LifecycleConnected, PQ: mesh.PQProtected, Peers: []mesh.Peer{{
		Lifecycle: mesh.LifecycleConnected, Path: mesh.PathDirect, PQ: mesh.PQProtected,
		PQVerifiedAt: now.Add(-3 * time.Minute).Format(time.RFC3339Nano),
	}}}
	report := TunnelReportFromRuntime(status, now)
	if report.SecurityState == "quantum_protected" || report.PQVerifiedAt != "" || report.DetailCode != "strict_pq_evidence_unavailable" {
		t.Fatalf("stale evidence claimed protection: %#v", report)
	}
	empty := TunnelReportFromRuntime(mesh.Status{}, now)
	if empty.AuthStage == "connected" || empty.Path.Type != "none" || empty.SecurityState == "quantum_protected" {
		t.Fatalf("empty evidence claimed a tunnel: %#v", empty)
	}
}

func TestTunnelPathVocabularyIsProviderNeutral(t *testing.T) {
	for input, want := range map[mesh.PathKind]string{mesh.PathUnknown: "none", mesh.PathDirect: "nexal_direct_internet", mesh.PathRelay: "nexal_relay", mesh.PathCloud: "nexal_cloud_route"} {
		if got := platformPath(input); got != want {
			t.Fatalf("%q mapped to %q, want %q", input, got, want)
		}
	}
}
