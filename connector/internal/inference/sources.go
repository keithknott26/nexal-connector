package inference

import (
	"context"
	"time"
)

// Host is one Mac as the data sources report it. Measured quantities that can be
// absent carry a *Known flag instead of a zero that would read as a measurement.
type Host struct {
	ID     string
	Name   string
	IsSelf bool
	Online bool

	Chip       string // "Apple M4 Pro"
	OS         string // "macOS 27.0 (27A5218g)"
	MacOSMajor int
	MacOSMinor int

	TotalMemoryBytes     uint64
	AvailableMemoryBytes uint64
	MemoryKnown          bool

	// AdmissionKnown is true when ApprovedMemoryBytes and OwnerReserveBytes come
	// from the connector's own resource policy (this Mac only). For another Mac
	// they are not visible remotely and the engine substitutes labelled defaults.
	AdmissionKnown      bool
	ApprovedMemoryBytes uint64
	OwnerReserveBytes   uint64

	DiskFreeBytes    uint64
	DiskKnown        bool
	DiskReserveBytes uint64 // owner's free-disk floor (policy minFreeDiskBytes); 0 = unknown

	// OffersCompute is nil when unknown (it is, for every peer today).
	OffersCompute *bool
}

// Link is this Mac's link to one peer, from the mesh status.
type Link struct {
	PeerID            string
	Lifecycle         string // connected | degraded | authenticating | offline ...
	Path              string // direct | relay | cloud | unknown
	PathLabel         string
	DirectVia         string
	LatencyMS         float64
	LatencyKnown      bool
	BandwidthMbps     float64
	BandwidthKnown    bool
	PacketLossPercent float64
	LossKnown         bool
	PQ                string // protected | degraded | unsupported ...
	PQReason          string
	PathFlapsLastHour int
}

// HostSource lists this Mac first and then the peers it can see.
type HostSource interface {
	Hosts(ctx context.Context) ([]Host, error)
}

// LinkSource lists this Mac's links to peers.
type LinkSource interface {
	Links(ctx context.Context) ([]Link, error)
}

// RuntimeProbe is the parsed output of the MLX runtime's `probe` command plus
// the outcome of the checks made before running it.
type RuntimeProbe struct {
	State                string
	Detail               string
	RuntimeVersion       string
	System               string
	Machine              string
	MLXVersion           string
	MLXLMVersion         string
	TransformersVersion  string
	DistributedExecution string
}

// RuntimeProber probes THIS Mac's runtime. Other Macs cannot be probed from here.
type RuntimeProber interface {
	Probe(ctx context.Context) RuntimeProbe
}

// StatusExtras carries connector facts that are not per-host.
type StatusExtras struct {
	// Reachable is false when the connector's local API could not be read.
	Reachable bool
	// ExecutionBlocker is the agent's own first reason it will not run jobs.
	ExecutionBlocker string
	MeshPQ           string
	Paused           bool
}

// ExtrasSource is optional: sources that read the connector status implement it.
type ExtrasSource interface {
	Extras() StatusExtras
}

// Engine runs the preflight. Every dependency is injected.
type Engine struct {
	Hosts   HostSource
	Links   LinkSource
	Runtime RuntimeProber
	Catalog CatalogFile
	Now     func() time.Time
}
