package client

import (
	"context"
	"errors"
	"math"
	"time"

	"nexal/connector/internal/mesh"
)

type NegotiatedSecurity struct {
	Algorithm  string `json:"algorithm"`
	Category   int    `json:"category"`
	Profile    string `json:"profile"`
	VerifiedAt string `json:"verifiedAt"`
	ExpiresAt  string `json:"expiresAt"`
}

type PeerSecurityReport struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Connected          bool                `json:"connected"`
	NegotiatedSecurity *NegotiatedSecurity `json:"negotiatedSecurity,omitempty"`
}

type TunnelStatusReport struct {
	PeerSecurity       []PeerSecurityReport `json:"peerSecurity"`
	NegotiatedSecurity *NegotiatedSecurity  `json:"negotiatedSecurity,omitempty"`
	AuthStage          string               `json:"authStage"`
	SecurityState      string               `json:"securityState"`
	Path               TunnelPath           `json:"path"`
	Traffic            TunnelTraffic        `json:"traffic"`
	LastHandshakeAt    string               `json:"lastHandshakeAt,omitempty"`
	LastTrafficAt      string               `json:"lastTrafficAt,omitempty"`
	PQVerifiedAt       string               `json:"pqVerifiedAt,omitempty"`
	DetailCode         string               `json:"detailCode,omitempty"`
	// Services this computer offers (ssh, vnc, smb). A pointer so that "none"
	// is sent as [] (closing their ports) while an unset value is omitted.
	Services *[]string `json:"services,omitempty"`
	// CoordinatorTraffic is separate from mesh peer traffic. The HTTP client
	// does not currently expose trustworthy wire-byte counters, so Available is
	// false and both counters are omitted. Never encode unknown as measured zero.
	CoordinatorTraffic CoordinatorTraffic `json:"coordinatorTraffic"`
}

type CoordinatorTraffic struct {
	Available     bool    `json:"available"`
	BytesSent     *uint64 `json:"bytesSent,omitempty"`
	BytesReceived *uint64 `json:"bytesReceived,omitempty"`
	Reason        string  `json:"reason,omitempty"`
}

type TunnelPath struct {
	Type        string  `json:"type"`
	RelayRegion string  `json:"relayRegion,omitempty"`
	LatencyMS   uint64  `json:"latencyMs,omitempty"`
	LossPercent float64 `json:"lossPercent,omitempty"`
}
type TunnelTraffic struct {
	BytesSent     uint64 `json:"bytesSent"`
	BytesReceived uint64 `json:"bytesReceived"`
}

func TunnelReportFromRuntime(status mesh.Status, now time.Time) TunnelStatusReport {
	status = mesh.SanitizeSnapshot(status)
	report := TunnelStatusReport{AuthStage: "not_started", SecurityState: "unknown", Path: TunnelPath{Type: "none"},
		CoordinatorTraffic: CoordinatorTraffic{Available: false, Reason: "wire byte counters are not available from this connector build"}}
	switch status.Lifecycle {
	case mesh.LifecycleProvisioning:
		report.AuthStage = "provisioning"
	case mesh.LifecycleAuthenticating:
		report.AuthStage = "authorizing"
	case mesh.LifecycleConnected, mesh.LifecycleDegraded:
		report.AuthStage = "connected"
	case mesh.LifecycleFailed:
		report.AuthStage = "failed"
	}
	// The claim is about the gateway link (see mesh.GatewayPQReadyAt); per-peer links
	// are reported individually in PeerSecurity below.
	if status.GatewayPQReadyAt(now, 2*time.Minute) {
		report.SecurityState = "quantum_protected"
	} else {
		switch status.PQ {
		case mesh.PQNegotiating:
			report.SecurityState = "negotiating"
		case mesh.PQRekeying:
			report.SecurityState = "rekeying"
		case mesh.PQFailed:
			report.SecurityState = "failed"
		case mesh.PQUnsupported:
			report.SecurityState = "unsupported"
		default:
			report.SecurityState = "degraded"
		}
	}
	report.PeerSecurity = make([]PeerSecurityReport, 0, len(status.Peers))
	for _, p := range status.Peers {
		item := PeerSecurityReport{ID: p.ID, Name: p.Name, Connected: p.Lifecycle == mesh.LifecycleConnected}
		single := mesh.Status{PQ: p.PQ, Peers: []mesh.Peer{p}}
		if single.StrictPQReadyAt(now, 2*time.Minute) {
			item.NegotiatedSecurity = &NegotiatedSecurity{Algorithm: "ML-KEM-1024", Category: 5, Profile: p.QuantumProfile, VerifiedAt: wireTimestamp(p.PQVerifiedAt), ExpiresAt: wireTimestamp(p.PQExpiresAt)}
		}
		report.PeerSecurity = append(report.PeerSecurity, item)
	}
	peer := bestRuntimePeer(status.Peers)
	if peer == nil {
		report.DetailCode = "runtime_evidence_unavailable"
		return report
	}
	report.Path = TunnelPath{Type: platformPath(peer.Path), RelayRegion: peer.RelayRegion,
		LatencyMS: uint64(math.Max(0, math.Round(peer.LatencyMS))), LossPercent: math.Max(0, math.Min(100, peer.PacketLossPercent))}
	report.Traffic = TunnelTraffic{BytesSent: peer.Traffic.SentBytes, BytesReceived: peer.Traffic.ReceivedBytes}
	report.LastHandshakeAt = wireTimestamp(peer.LastHandshakeAt)
	report.LastTrafficAt = wireTimestamp(peer.Traffic.LastAt)
	if report.SecurityState == "quantum_protected" {
		// Evidence comes from the gateway link the claim is about, not the lowest-latency peer.
		evidence := peer
		for i := range status.Peers {
			if mesh.IsGatewayPeer(status.Peers[i].Name) && status.Peers[i].PQ == mesh.PQProtected {
				evidence = &status.Peers[i]
				break
			}
		}
		report.PQVerifiedAt = wireTimestamp(evidence.PQVerifiedAt)
		report.NegotiatedSecurity = &NegotiatedSecurity{Algorithm: "ML-KEM-1024", Category: 5, Profile: evidence.QuantumProfile, VerifiedAt: report.PQVerifiedAt, ExpiresAt: wireTimestamp(evidence.PQExpiresAt)}
	}
	if report.SecurityState != "quantum_protected" {
		report.DetailCode = "strict_pq_evidence_unavailable"
	}
	return report
}

func bestRuntimePeer(peers []mesh.Peer) *mesh.Peer {
	var best *mesh.Peer
	for i := range peers {
		if peers[i].Lifecycle != mesh.LifecycleConnected && peers[i].Lifecycle != mesh.LifecycleDegraded {
			continue
		}
		if best == nil || (peers[i].LatencyMS > 0 && (best.LatencyMS <= 0 || peers[i].LatencyMS < best.LatencyMS)) {
			best = &peers[i]
		}
	}
	return best
}

func platformPath(path mesh.PathKind) string {
	switch path {
	case mesh.PathDirect:
		return "nexal_direct_internet"
	case mesh.PathRelay:
		return "nexal_relay"
	case mesh.PathCloud:
		return "nexal_cloud_route"
	default:
		return "none"
	}
}

func wireTimestamp(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func (c *Client) ReportTunnelStatus(ctx context.Context, hostID string, status mesh.Status) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	report := TunnelReportFromRuntime(status, time.Now())
	services := append([]string{}, mesh.LocalServices()...)
	report.Services = &services
	var out struct {
		OK bool `json:"ok"`
	}
	// Lenient: the coordinator once returned extra fields here, and strict decoding
	// turned every accepted report into "invalid coordinator response schema".
	if err := c.callLenient(ctx, "POST", "/api/v2/devices/"+hostID+"/tunnel-status", report, &out); err != nil {
		return err
	}
	if !out.OK {
		return errors.New("coordinator did not accept tunnel status")
	}
	return nil
}
