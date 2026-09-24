package mesh

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// RuntimeProvider reads the privileged overlay's live status. It deliberately
// converts the implementation-specific response at this boundary so neither
// customer-visible JSON nor the native application needs to know which runtime
// supplies the mesh.
type RuntimeProvider struct {
	Executable string
	Timeout    time.Duration
}

func NewRuntimeProvider() RuntimeProvider { return RuntimeProvider{Timeout: 3 * time.Second} }

type runtimeStatus struct {
	DaemonStatus      string `json:"daemonStatus"`
	QuantumResistance bool   `json:"quantumResistance"`
	Management        struct {
		Connected bool `json:"connected"`
	} `json:"management"`
	Signal struct {
		Connected bool `json:"connected"`
	} `json:"signal"`
	Peers struct {
		Details []struct {
			FQDN              string        `json:"fqdn"`
			IP                string        `json:"netbirdIp"`
			Status            string        `json:"status"`
			ConnectionType    string        `json:"connectionType"`
			RelayAddress      string        `json:"relayAddress"`
			LastHandshake     time.Time     `json:"lastWireguardHandshake"`
			Received          int64         `json:"transferReceived"`
			Sent              int64         `json:"transferSent"`
			Latency           time.Duration `json:"latency"`
			QuantumResistance bool          `json:"quantumResistance"`
		} `json:"details"`
	} `json:"peers"`
}

func (p RuntimeProvider) Snapshot() Status {
	path := p.Executable
	if path == "" {
		var err error
		path, err = trustedExecutable("nexal-network")
		if err != nil {
			return UnavailableProvider{}.Snapshot()
		}
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "status", "--json").Output()
	if err != nil {
		return UnavailableProvider{}.Snapshot()
	}
	var raw runtimeStatus
	if json.Unmarshal(out, &raw) != nil {
		return UnavailableProvider{}.Snapshot()
	}

	now := time.Now().UTC()
	status := Status{ProviderAvailable: true, Lifecycle: LifecycleAuthenticating,
		AuthenticationStep: "Connecting to the neXal network", PQ: PQNegotiating,
		UpdatedAt: FreshRFC3339(now), Peers: []Peer{}}
	if raw.DaemonStatus == "Connected" && raw.Management.Connected && raw.Signal.Connected {
		status.Lifecycle = LifecycleConnected
		status.AuthenticationStep = "Authenticated"
	}
	allPQ := raw.QuantumResistance
	for _, item := range raw.Peers.Details {
		connected := strings.EqualFold(item.Status, "connected")
		peer := Peer{ID: item.IP, Name: runtimePeerName(item.FQDN),
			Lifecycle: LifecycleAuthenticating, AuthenticationStep: item.Status,
			Path: PathUnknown, PathLabel: "Route unavailable", PQ: PQNegotiating,
			Traffic:  Traffic{ReceivedBytes: nonnegative(item.Received), SentBytes: nonnegative(item.Sent)},
			Hostname: HostnameStatus{State: "unavailable", Detail: "Private neXal hostname is being provisioned."}}
		if connected {
			peer.Lifecycle = LifecycleConnected
			peer.AuthenticationStep = "Authenticated"
			switch strings.ToLower(item.ConnectionType) {
			case "p2p":
				peer.Path, peer.PathLabel = PathDirect, "P2P — direct"
			case "relayed":
				peer.Path, peer.PathLabel = PathRelay, "neXal Relay — metered"
			}
			if item.QuantumResistance {
				peer.PQ = PQProtected
				peer.PQVerifiedAt = FreshRFC3339(now)
			} else {
				peer.PQ = PQDegraded
				allPQ = false
			}
			if !item.LastHandshake.IsZero() {
				peer.LastHandshakeAt = FreshRFC3339(item.LastHandshake)
			}
			peer.LatencyMS = float64(item.Latency) / float64(time.Millisecond)
		} else {
			allPQ = false
		}
		status.Peers = append(status.Peers, peer)
	}
	if status.Lifecycle == LifecycleConnected && allPQ {
		status.PQ = PQProtected
	} else if status.Lifecycle == LifecycleConnected {
		status.PQ = PQDegraded
	}
	return status
}

func runtimePeerName(fqdn string) string {
	name, _, _ := strings.Cut(fqdn, ".")
	if name == "" {
		return "Connected computer"
	}
	return name
}

func nonnegative(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}
