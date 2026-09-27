package mesh

import (
	"context"
	"encoding/json"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// RuntimeProvider reads the installed secure-networking runtime's own status
// (`nexal-network status --json`) and translates it into neXal terms. It is
// read-only: it never starts, stops, or reconfigures the runtime.
//
// Without a provider the agent reports "unavailable" forever, and the
// coordinator never sees a connected tunnel, so a pairing can never finish.
type RuntimeProvider struct {
	Timeout time.Duration
	// Run returns the runtime's JSON status. Replaced in tests.
	Run func(ctx context.Context) ([]byte, error)
	Now func() time.Time
}

func NewRuntimeProvider() RuntimeProvider {
	return RuntimeProvider{Timeout: 5 * time.Second, Run: runRuntimeStatus, Now: time.Now}
}

func runRuntimeStatus(ctx context.Context) ([]byte, error) {
	path, err := trustedExecutable("nexal-network")
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path, "status", "--json").Output()
}

type runtimeStatus struct {
	Management struct {
		Connected bool `json:"connected"`
	} `json:"management"`
	QuantumResistance bool   `json:"quantumResistance"`
	NetbirdIP         string `json:"netbirdIp"`
	Peers             struct {
		Details []runtimePeer `json:"details"`
	} `json:"peers"`
}

const experimentalQuantumProfile = "nexal-mlkem1024-tcp-v2"

// Only the runtime's active, generation-bound WireGuard session supplies this evidence.
// Configuration flags and ordinary WireGuard handshakes are insufficient.
func validQuantumEvidence(profile, installed, expires string, now time.Time) (time.Time, bool) {
	i, ie := time.Parse(time.RFC3339Nano, installed)
	e, ee := time.Parse(time.RFC3339Nano, expires)
	return i, profile == experimentalQuantumProfile && ie == nil && ee == nil &&
		!i.After(now) && e.After(now) && e.After(i) && e.Sub(i) <= 3*time.Minute && now.Sub(i) <= 2*time.Minute
}

type runtimePeer struct {
	QuantumProfile        string          `json:"quantumProfile"`
	QuantumKeyInstalledAt string          `json:"quantumKeyInstalledAt"`
	QuantumKeyExpiresAt   string          `json:"quantumKeyExpiresAt"`
	FQDN                  string          `json:"fqdn"`
	NetbirdIP             string          `json:"netbirdIp"`
	PublicKey             string          `json:"publicKey"`
	Status                string          `json:"status"`
	ConnectionType        string          `json:"connectionType"`
	LastHandshake         string          `json:"lastWireguardHandshake"`
	TransferReceived      int64           `json:"transferReceived"`
	TransferSent          int64           `json:"transferSent"`
	QuantumResistance     bool            `json:"quantumResistance"`
	Latency               json.RawMessage `json:"latency"`
	ICECandidateType      struct {
		Local  string `json:"local"`
		Remote string `json:"remote"`
	} `json:"iceCandidateType"`
	ICECandidateEndpoint struct {
		Local  string `json:"local"`
		Remote string `json:"remote"`
	} `json:"iceCandidateEndpoint"`
}

func (p RuntimeProvider) Snapshot() Status {
	if p.Run == nil {
		return UnavailableProvider{}.Snapshot()
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := p.Run(ctx)
	if err != nil {
		return UnavailableProvider{}.Snapshot()
	}
	return translateRuntime(out, now())
}

func translateRuntime(out []byte, now time.Time) Status {
	var rs runtimeStatus
	if err := json.Unmarshal(out, &rs); err != nil {
		return UnavailableProvider{}.Snapshot()
	}
	s := Status{ProviderAvailable: true, Lifecycle: LifecycleAuthenticating, PQ: PQUnsupported,
		UpdatedAt: FreshRFC3339(now), Peers: []Peer{}}
	if ip, _, _ := strings.Cut(rs.NetbirdIP, "/"); ip != "" {
		s.SelfTunnelAddress = ip
	}
	if rs.Management.Connected {
		// This Mac is on its network. Peers are reported separately: the first
		// Mac in a network has none, and must still be able to finish pairing.
		s.Lifecycle = LifecycleConnected
	}
	allProtected := true
	for _, rp := range rs.Peers.Details {
		peer := Peer{ID: rp.PublicKey, Name: shortName(rp.FQDN), Path: PathUnknown, PQ: PQDegraded,
			Traffic: Traffic{ReceivedBytes: nonNeg(rp.TransferReceived), SentBytes: nonNeg(rp.TransferSent)}}
		if ip, _, _ := strings.Cut(rp.NetbirdIP, "/"); ip != "" {
			peer.TunnelAddress = ip
		}
		switch strings.ToLower(rp.Status) {
		case "connected":
			peer.Lifecycle = LifecycleConnected
		case "connecting":
			peer.Lifecycle = LifecycleAuthenticating
		default: // idle: lazy connection, no tunnel until traffic needs one
			peer.Lifecycle = LifecycleUnavailable
		}
		switch strings.ToLower(rp.ConnectionType) {
		case "p2p":
			peer.Path, peer.PathLabel = PathDirect, "P2P — direct"
			// The address the encrypted tunnel actually reaches the peer on: a
			// LAN address when both Macs share a network, otherwise the peer's
			// public (NAT) address.
			if ap, err := netip.ParseAddrPort(rp.ICECandidateEndpoint.Remote); err == nil {
				peer.DirectAddress = ap.Addr().Unmap().String()
				peer.DirectIsPrivate = ap.Addr().IsPrivate() || ap.Addr().IsLinkLocalUnicast()
			}
			switch {
			case rp.ICECandidateType.Local == "host" && rp.ICECandidateType.Remote == "host":
				peer.DirectVia = "lan"
			case rp.ICECandidateType.Remote != "":
				peer.DirectVia = "nat"
			}
		case "relayed":
			peer.Path, peer.PathLabel = PathRelay, "neXal Relay — metered"
		}
		if t, err := time.Parse(time.RFC3339Nano, rp.LastHandshake); err == nil && t.Year() > 2000 {
			peer.LastHandshakeAt = FreshRFC3339(t)
		}
		var ns float64
		if json.Unmarshal(rp.Latency, &ns) == nil && ns > 0 {
			peer.LatencyMS = ns / 1e6
		}
		installed, verified := validQuantumEvidence(rp.QuantumProfile, rp.QuantumKeyInstalledAt, rp.QuantumKeyExpiresAt, now)
		if peer.Lifecycle == LifecycleConnected && rs.QuantumResistance && rp.QuantumResistance && verified {
			peer.PQ, peer.PQVerifiedAt = PQProtected, installed.Format(time.RFC3339Nano)
			peer.QuantumProfile, peer.PQExpiresAt = rp.QuantumProfile, rp.QuantumKeyExpiresAt
		} else {
			allProtected = false
		}
		if peer.Lifecycle == LifecycleConnected && peer.TunnelAddress != "" {
			peer.Services = probeServices(peer.TunnelAddress)
		}
		s.Peers = append(s.Peers, peer)
	}
	switch {
	case len(s.Peers) > 0 && allProtected:
		s.PQ = PQProtected
	case rs.QuantumResistance && len(s.Peers) == 0:
		s.PQ = PQNegotiating
	case rs.QuantumResistance:
		s.PQ = PQDegraded
	}
	return s
}

// shortName keeps only the device label so no upstream domain is exposed.
func shortName(fqdn string) string {
	if i := strings.IndexByte(fqdn, '.'); i > 0 {
		return fqdn[:i]
	}
	return fqdn
}

func nonNeg(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
