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
)

type PathKind string

const (
	PathUnknown PathKind = "unknown"
	PathDirect  PathKind = "direct"
	PathRelay   PathKind = "relay"
	PathCloud   PathKind = "cloud"
)

type PQState string

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
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Lifecycle          Lifecycle      `json:"lifecycle"`
	AuthenticationStep string         `json:"authenticationStep,omitempty"`
	Path               PathKind       `json:"path"`
	PathLabel          string         `json:"pathLabel"`
	RelayRegion        string         `json:"relayRegion,omitempty"`
	LatencyMS          float64        `json:"latencyMs,omitempty"`
	PacketLossPercent  float64        `json:"packetLossPercent,omitempty"`
	LastHandshakeAt    string         `json:"lastHandshakeAt,omitempty"`
	PQ                 PQState        `json:"pq"`
	PQVerifiedAt       string         `json:"pqVerifiedAt,omitempty"`
	Traffic            Traffic        `json:"traffic"`
	FileSharing        FileSharing    `json:"fileSharing"`
	ScreenSharing      ScreenSharing  `json:"screenSharing"`
	Hostname           HostnameStatus `json:"hostname"`
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
		verified, err := time.Parse(time.RFC3339Nano, peer.PQVerifiedAt)
		if peer.PQ != PQProtected || err != nil || verified.After(now.Add(5*time.Second)) || now.Sub(verified) > 2*time.Minute {
			peer.FileSharing.Available, peer.FileSharing.Address = false, ""
			peer.ScreenSharing.Available, peer.ScreenSharing.Address = false, ""
			if peer.Lifecycle == LifecycleConnected {
				peer.Lifecycle = LifecycleDegraded
			}
		}
	}
	return s
}

func (s Status) StrictPQReady() bool {
	return s.StrictPQReadyAt(time.Now(), 2*time.Minute)
}

func (s Status) StrictPQReadyAt(now time.Time, maxAge time.Duration) bool {
	if s.PQ != PQProtected || len(s.Peers) == 0 {
		return false
	}
	for _, peer := range s.Peers {
		verified, err := time.Parse(time.RFC3339Nano, peer.PQVerifiedAt)
		if peer.Lifecycle != LifecycleConnected || peer.PQ != PQProtected || err != nil || verified.After(now.Add(5*time.Second)) || now.Sub(verified) > maxAge {
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
