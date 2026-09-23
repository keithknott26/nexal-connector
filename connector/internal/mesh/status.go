// Package mesh defines the vendor-neutral boundary between neXal and the
// privileged overlay implementation. It deliberately contains no installer or
// process supervision: production networking must be installed and authorized
// separately before a provider can report anything other than unavailable.
package mesh

import "time"

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
	Traffic            Traffic   `json:"traffic"`
}

type Status struct {
	ProviderAvailable  bool      `json:"providerAvailable"`
	Lifecycle          Lifecycle `json:"lifecycle"`
	AuthenticationStep string    `json:"authenticationStep,omitempty"`
	PQ                 PQState   `json:"pq"`
	UpdatedAt          string    `json:"updatedAt,omitempty"`
	Peers              []Peer    `json:"peers"`
}

// Provider is the only dependency neXal takes on a mesh implementation.
// Implementations must return sanitized product terminology and must never
// expose upstream product names, raw relay hosts, setup keys, or private keys.
type Provider interface{ Snapshot() Status }

type UnavailableProvider struct{}

func (UnavailableProvider) Snapshot() Status {
	return Status{Lifecycle: LifecycleUnavailable, PQ: PQUnsupported, Peers: []Peer{}}
}

func FreshRFC3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
