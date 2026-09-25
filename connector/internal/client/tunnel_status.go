package client

import (
	"context"
	"errors"
	"math"
	"time"

	"nexal/connector/internal/mesh"
	"nexal/connector/internal/remoteservice"
)

type TunnelStatusReport struct {
	AuthStage       string        `json:"authStage"`
	SecurityState   string        `json:"securityState"`
	Path            TunnelPath    `json:"path"`
	Traffic         TunnelTraffic `json:"traffic"`
	LastHandshakeAt string        `json:"lastHandshakeAt,omitempty"`
	LastTrafficAt   string        `json:"lastTrafficAt,omitempty"`
	PQVerifiedAt    string        `json:"pqVerifiedAt,omitempty"`
	DetailCode      string        `json:"detailCode,omitempty"`
	// Services this computer offers (ssh, vnc, smb). A pointer so that "none"
	// is sent as [] (closing their ports) while an unset value is omitted.
	Services *[]string `json:"services,omitempty"`
	// ServiceCapabilities is additive to Services for older coordinators. It
	// distinguishes observation from authorization; neither is a request to
	// enable a macOS service.
	ServiceCapabilities []remoteservice.Capability `json:"serviceCapabilities,omitempty"`
	// Peers preserves the per-peer evidence needed by iOS/macOS graphs. The
	// scalar Path and Traffic above remain for deployed coordinator versions.
	Peers []TunnelPeerReport `json:"peers,omitempty"`
	// ControlPlaneTraffic is intentionally absent until the HTTP transport owns
	// trustworthy counters. Peer totals must not be mislabeled coordinator I/O.
	ControlPlaneTraffic *TunnelTraffic `json:"controlPlaneTraffic,omitempty"`
}

type TunnelPeerReport struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Lifecycle       string        `json:"lifecycle"`
	Path            TunnelPath    `json:"path"`
	Traffic         TunnelTraffic `json:"traffic"`
	LastHandshakeAt string        `json:"lastHandshakeAt,omitempty"`
	LastTrafficAt   string        `json:"lastTrafficAt,omitempty"`
	PQVerifiedAt    string        `json:"pqVerifiedAt,omitempty"`
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
	report := TunnelStatusReport{AuthStage: "not_started", SecurityState: "unknown", Path: TunnelPath{Type: "none"}}
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
	if status.StrictPQReadyAt(now, 2*time.Minute) {
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
	for _, peer := range status.Peers {
		if peer.Lifecycle != mesh.LifecycleConnected && peer.Lifecycle != mesh.LifecycleDegraded {
			continue
		}
		item := TunnelPeerReport{ID: peer.ID, Name: peer.Name, Lifecycle: string(peer.Lifecycle),
			Path: TunnelPath{Type: platformPath(peer.Path), RelayRegion: peer.RelayRegion,
				LatencyMS: uint64(math.Max(0, math.Round(peer.LatencyMS))), LossPercent: math.Max(0, math.Min(100, peer.PacketLossPercent))},
			Traffic:         TunnelTraffic{BytesSent: peer.Traffic.SentBytes, BytesReceived: peer.Traffic.ReceivedBytes},
			LastHandshakeAt: wireTimestamp(peer.LastHandshakeAt), LastTrafficAt: wireTimestamp(peer.Traffic.LastAt)}
		if peer.PQ == mesh.PQProtected {
			item.PQVerifiedAt = wireTimestamp(peer.PQVerifiedAt)
		}
		report.Peers = append(report.Peers, item)
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
		report.PQVerifiedAt = wireTimestamp(peer.PQVerifiedAt)
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
	report.ServiceCapabilities = remoteservice.Capabilities(services, nil, time.Now())
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
