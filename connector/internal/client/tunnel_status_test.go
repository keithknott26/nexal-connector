package client

import (
	"testing"
	"time"

	"nexal/connector/internal/mesh"
)

func TestTunnelReportMapsOnlyFreshVerifiedRuntimeEvidence(t *testing.T) {
	now := time.Now().UTC()
	status := mesh.Status{Lifecycle: mesh.LifecycleConnected, PQ: mesh.PQProtected, Peers: []mesh.Peer{{
		ID: "peer", Lifecycle: mesh.LifecycleConnected, Path: mesh.PathRelay, RelayRegion: "New York",
		QuantumProfile: "nexal-mlkem1024-tcp-v2", PQExpiresAt: now.Add(3 * time.Minute).Format(time.RFC3339Nano),
		LatencyMS: 31.4, PacketLossPercent: .2, PQ: mesh.PQProtected, PQVerifiedAt: now.Format(time.RFC3339Nano),
		LastHandshakeAt: now.Add(-time.Second).Format(time.RFC3339Nano),
		Traffic:         mesh.Traffic{SentBytes: 12, ReceivedBytes: 34, LastAt: now.Format(time.RFC3339Nano)},
	}}}
	report := TunnelReportFromRuntime(status, now)
	if report.AuthStage != "connected" || report.SecurityState != "quantum_protected" || report.Path.Type != "nexal_relay" || report.Path.RelayRegion != "New York" || report.PQVerifiedAt == "" {
		t.Fatalf("verified runtime evidence mapped incorrectly: %#v", report)
	}
	if report.NegotiatedSecurity == nil || report.NegotiatedSecurity.Profile != "nexal-mlkem1024-tcp-v2" || report.NegotiatedSecurity.Category != 5 || report.NegotiatedSecurity.ExpiresAt == "" {
		t.Fatalf("negotiated evidence missing: %#v", report.NegotiatedSecurity)
	}
	if report.Traffic.BytesSent != 12 || report.Traffic.BytesReceived != 34 {
		t.Fatalf("traffic lost: %#v", report.Traffic)
	}
}

func TestTunnelReportFailsClosedForStaleOrMissingEvidence(t *testing.T) {
	now := time.Now().UTC()
	status := mesh.Status{Lifecycle: mesh.LifecycleConnected, PQ: mesh.PQProtected, Peers: []mesh.Peer{{
		Lifecycle: mesh.LifecycleConnected, Path: mesh.PathDirect, PQ: mesh.PQProtected,
		PQVerifiedAt: now.Add(-3 * time.Minute).Format(time.RFC3339Nano),
	}}}
	report := TunnelReportFromRuntime(status, now)
	if report.NegotiatedSecurity != nil || report.SecurityState == "quantum_protected" || report.PQVerifiedAt != "" || report.DetailCode != "strict_pq_evidence_unavailable" {
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
