// Package mesh defines the vendor-neutral boundary between neXal and the
// privileged overlay implementation. It deliberately contains no installer or
// process supervision: production networking must be installed and authorized
// separately before a provider can report anything other than unavailable.
package mesh

import (
	"strings"
	"time"
)

type Lifecycle string

const (
	LifecycleUnavailable    Lifecycle = "unavailable"
	LifecycleProvisioning   Lifecycle = "provisioning"
	LifecycleAuthenticating Lifecycle = "authenticating"
	LifecycleConnected      Lifecycle = "connected"
	LifecycleDegraded       Lifecycle = "degraded"
	LifecycleFailed         Lifecycle = "failed"
	// LifecycleOffline is a peer the runtime has been trying to reach for a while with no
	// recent handshake: switched off, asleep, out of battery or off the internet. Distinct
	// from authenticating, which is a reconnect in progress.
	LifecycleOffline Lifecycle = "offline"
)

// Thresholds for calling a peer offline rather than "connecting".
const (
	OfflineAfterConnecting = 90 * time.Second
	OfflineHandshakeAge    = 3 * time.Minute
)

// PeerLooksOffline: the runtime says "connecting", it has been saying so for longer than
// OfflineAfterConnecting (or for an unknown time), and the last WireGuard handshake is
// missing or older than OfflineHandshakeAge.
func PeerLooksOffline(statusSince, lastHandshake time.Time, now time.Time) bool {
	if !statusSince.IsZero() && now.Sub(statusSince) < OfflineAfterConnecting {
		return false
	}
	return lastHandshake.IsZero() || now.Sub(lastHandshake) > OfflineHandshakeAge
}

type PathKind string

const (
	PathUnknown PathKind = "unknown"
	PathDirect  PathKind = "direct"
	PathRelay   PathKind = "relay"
	PathCloud   PathKind = "cloud"
)

type PQState string

// Machine-readable reasons for a link that is not protected. The runtime
// supplies the first group (they name what the key exchange observed); the
// connector derives the second group from the status it can see itself.
const (
	// Reported by the runtime's key-exchange layer.
	PQReasonExchangePending  = "exchange-pending"   // registered, no exchange completed yet
	PQReasonPeerUnreachable  = "peer-unreachable"   // control listener could not be reached; retries back off
	PQReasonPeerLacksProfile = "peer-lacks-profile" // peer never advertises this profile (phone, stock or older runtime)
	PQReasonEvidenceExpired  = "evidence-expired"   // lease ended without renewal; gate closed until a fresh exchange
	PQReasonKeyInstallFailed = "key-install-failed" // exchange completed but the local gate refused the key
	PQReasonSessionPending   = "session-pending"    // key installed, tunnel session for it not yet established
	// Derived by the connector.
	PQReasonPeerDisconnected = "peer-disconnected"  // no tunnel to the peer right now
	PQReasonEvidenceStale    = "evidence-stale"     // evidence present but older than the connector's freshness ceiling
	PQReasonRuntimeNotStrict = "runtime-not-strict" // local runtime is not in strict ML-KEM mode
)

const (
	PQUnsupported PQState = "unsupported"
	PQNegotiating PQState = "negotiating"
	PQProtected   PQState = "protected"
	PQRekeying    PQState = "rekeying"
	PQDegraded    PQState = "degraded"
	PQStale       PQState = "verification_stale"
	PQFailed      PQState = "failed"
)

type Traffic struct {
	ReceivedBytes uint64 `json:"receivedBytes"`
	SentBytes     uint64 `json:"sentBytes"`
	LastAt        string `json:"lastAt,omitempty"`
}

// FileSharing is reported by the privileged provider only after platform policy
// authorizes SMB for this peer. Address is a sanitized private neXal hostname;
// it must never be inferred from an observed public endpoint.
type FileSharing struct {
	Authorized bool   `json:"authorized"`
	Available  bool   `json:"available"`
	Address    string `json:"address,omitempty"`
	ShareName  string `json:"shareName,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type HostnameStatus struct {
	State    string `json:"state"` // unavailable, provisioned, resolving, ready, stale, conflict
	Hostname string `json:"hostname,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type ScreenSharing struct {
	Authorized bool   `json:"authorized"`
	Available  bool   `json:"available"`
	Address    string `json:"address,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type DiscoveryStatus struct {
	WideAreaBonjour bool   `json:"wideAreaBonjour"`
	Gateway         string `json:"gateway"` // unavailable, discovering, active, degraded
	Bridge          string `json:"bridge"`  // unavailable, connecting, active, degraded
	SiteID          string `json:"siteId,omitempty"`
	LastRecordAt    string `json:"lastRecordAt,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

type Peer struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Lifecycle          Lifecycle `json:"lifecycle"`
	AuthenticationStep string    `json:"authenticationStep,omitempty"`
	Path               PathKind  `json:"path"`
	PathLabel          string    `json:"pathLabel"`
	RelayRegion        string    `json:"relayRegion,omitempty"`
	LatencyMS          float64   `json:"latencyMs,omitempty"`
	PacketLossPercent  float64   `json:"packetLossPercent,omitempty"`
	LastHandshakeAt    string    `json:"lastHandshakeAt,omitempty"`
	PQ                 PQState   `json:"pq"`
	PQVerifiedAt       string    `json:"pqVerifiedAt,omitempty"`
	QuantumProfile     string    `json:"quantumProfile,omitempty"`
	PQExpiresAt        string    `json:"pqExpiresAt,omitempty"`
	// PQReason is the machine-readable explanation while PQ is not protected
	// (see PQReason* constants). Empty when the link is protected.
	PQReason string `json:"pqReason,omitempty"`
	// PathFlapsLastHour counts direct<->relay path changes in the last hour.
	// Local-only: client.TunnelReportFromRuntime never copies it.
	PathFlapsLastHour int            `json:"pathFlapsLastHour,omitempty"`
	Traffic           Traffic        `json:"traffic"`
	FileSharing       FileSharing    `json:"fileSharing"`
	ScreenSharing     ScreenSharing  `json:"screenSharing"`
	Hostname          HostnameStatus `json:"hostname"`
	// Local-only fields for the owner's own panel. The coordinator report is
	// built by client.TunnelReportFromRuntime, which never copies them.
	//
	// TunnelAddress is the peer's private tunnel IP.
	TunnelAddress string `json:"tunnelAddress,omitempty"`
	// DirectAddress is the peer endpoint of a P2P tunnel (no port); private when
	// both Macs share a LAN.
	DirectAddress   string `json:"directAddress,omitempty"`
	DirectIsPrivate bool   `json:"directIsPrivate,omitempty"`
	// DirectVia is how the P2P tunnel reaches the peer: "lan" (both ends used
	// local-network addresses), "nat" (through a router's public address), or
	// "" when unknown. From the runtime's selected ICE candidate pair.
	DirectVia string `json:"directVia,omitempty"`
	// Services lists what answered on the tunnel address: "ssh", "vnc", "smb".
	Services []string `json:"services,omitempty"`
	// BandwidthMbps is the most recent measured download throughput to this peer
	// in megabits per second. Populated by the bandwidth test runner, not the
	// mesh provider. Local-only: the coordinator report copies it into
	// PeerSecurityReport.BandwidthMbps but it is not part of the runtime's
	// native status.
	BandwidthMbps float64 `json:"bandwidthMbps,omitempty"`
}

type Status struct {
	ProviderAvailable  bool            `json:"providerAvailable"`
	Lifecycle          Lifecycle       `json:"lifecycle"`
	AuthenticationStep string          `json:"authenticationStep,omitempty"`
	PQ                 PQState         `json:"pq"`
	UpdatedAt          string          `json:"updatedAt,omitempty"`
	Peers              []Peer          `json:"peers"`
	Discovery          DiscoveryStatus `json:"discovery"`
	// SelfTunnelAddress is this host's own tunnel IP. Reported to the coordinator
	// only as the Wake-on-LAN lookup key (client.WakeInfo.TunnelAddress).
	SelfTunnelAddress string `json:"-"`
}

// Provider is the only dependency neXal takes on a mesh implementation.
// Implementations must return sanitized product terminology and must never
// expose upstream product names, raw relay hosts, setup keys, or private keys.
type Provider interface{ Snapshot() Status }

// SanitizeSnapshot is the final customer-surface boundary. It prevents a
// provider bug from exposing an upstream hostname or making an unauthorized
// sharing service actionable.
func SanitizeSnapshot(s Status) Status {
	now := time.Now()
	for i := range s.Peers {
		peer := &s.Peers[i]
		if !validHostnameState(peer.Hostname.State) || !isNexalHostname(peer.Hostname.Hostname) {
			peer.Hostname = HostnameStatus{State: "unavailable", Detail: "Private neXal hostname unavailable."}
		}
		if peer.Hostname.State != "ready" || !peer.FileSharing.Authorized || !peer.FileSharing.Available || peer.FileSharing.Address != peer.Hostname.Hostname {
			peer.FileSharing.Available, peer.FileSharing.Address = false, ""
		}
		if peer.Hostname.State != "ready" || !peer.ScreenSharing.Authorized || !peer.ScreenSharing.Available || peer.ScreenSharing.Address != peer.Hostname.Hostname {
			peer.ScreenSharing.Available, peer.ScreenSharing.Address = false, ""
		}
		_, valid := validQuantumEvidence(peer.QuantumProfile, peer.PQVerifiedAt, peer.PQExpiresAt, now)
		if peer.PQ != PQProtected || !valid {
			peer.FileSharing.Available, peer.FileSharing.Address = false, ""
			peer.ScreenSharing.Available, peer.ScreenSharing.Address = false, ""
			if peer.PQReason == PQReasonPeerLacksProfile || peer.PQReason == PQReasonRuntimeNotStrict {
				// A device that never carries the profile (a phone) or a runtime
				// that is not in strict ML-KEM mode is not a degraded link; it is
				// not covered. Its link stays unprotected (the gate never opens
				// for it), and it does not demote the host or the peer lifecycle.
				peer.PQ = PQUnsupported
				continue
			}
			peer.PQ = PQDegraded
			if peer.PQReason == "" {
				peer.PQReason = PQReasonEvidenceStale
			}
			if s.PQ == PQProtected {
				s.PQ = PQDegraded
			}
			if peer.Lifecycle == LifecycleConnected {
				peer.Lifecycle = LifecycleDegraded
			}
		} else {
			peer.PQReason = ""
		}
	}
	return s
}

// IsGatewayPeer reports whether a peer is a neXal gateway (storage and exit). The
// platform enrolls every gateway with the hostname "gw-<region>-<n>"
// (self-hosted-config/gateway/bootstrap.sh), and the runtime reports that label.
func IsGatewayPeer(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "gw-")
}

// GatewayPQReadyAt is the host-level post-quantum claim: every connected neXal
// gateway link carries fresh ML-KEM-1024 evidence, and at least one does. The
// gateway link is the one that carries backups and exit traffic off the owner's
// devices; links between the owner's own devices are reported per peer instead
// and do not change this claim.
func (s Status) GatewayPQReadyAt(now time.Time, maxAge time.Duration) bool {
	protected := false
	for _, peer := range s.Peers {
		if !IsGatewayPeer(peer.Name) || peer.Lifecycle != LifecycleConnected {
			continue
		}
		verified, valid := validQuantumEvidence(peer.QuantumProfile, peer.PQVerifiedAt, peer.PQExpiresAt, now)
		if peer.PQ != PQProtected || !valid || now.Sub(verified) > maxAge {
			return false
		}
		protected = true
	}
	return protected
}

func (s Status) StrictPQReady() bool {
	return s.StrictPQReadyAt(time.Now(), 5*time.Minute)
}

func (s Status) StrictPQReadyAt(now time.Time, maxAge time.Duration) bool {
	if s.PQ != PQProtected || len(s.Peers) == 0 {
		return false
	}
	for _, peer := range s.Peers {
		verified, valid := validQuantumEvidence(peer.QuantumProfile, peer.PQVerifiedAt, peer.PQExpiresAt, now)
		if peer.Lifecycle != LifecycleConnected || peer.PQ != PQProtected || !valid || now.Sub(verified) > maxAge {
			return false
		}
	}
	return true
}

func validHostnameState(state string) bool {
	switch state {
	case "provisioned", "resolving", "ready", "stale", "conflict":
		return true
	default:
		return false
	}
}

func isNexalHostname(host string) bool {
	return len(host) > len(".mesh.nexal.systems") && len(host) <= 253 &&
		strings.HasSuffix(strings.ToLower(host), ".mesh.nexal.systems") &&
		!strings.ContainsAny(host, "/\\@ \t\r\n")
}

type UnavailableProvider struct{}

func (UnavailableProvider) Snapshot() Status {
	return Status{Lifecycle: LifecycleUnavailable, PQ: PQUnsupported, Peers: []Peer{}}
}

func FreshRFC3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
