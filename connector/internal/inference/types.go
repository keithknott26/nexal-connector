package inference

// SchemaVersion is the version of the JSON Report. Consumers (the Mac app) must
// refuse a report whose schemaVersion they do not know. Additive fields do not
// bump it; renamed, removed or re-typed fields do.
const SchemaVersion = 1

// Verdict is the per-model result. The values are part of the JSON contract.
type Verdict string

const (
	// VerdictRunsNow: fits on one host whose MLX runtime probes healthy, and
	// that host is this Mac. Weights still have to be provisioned by the owner
	// (ModelProvisioning says so); "runs now" is about hardware and runtime.
	VerdictRunsNow Verdict = "runs_now"
	// VerdictFitsSingleBlocked: fits on one host, but something other than memory
	// (runtime not healthy, host is another Mac whose runtime cannot be checked
	// from here, low disk) stops it from running now.
	VerdictFitsSingleBlocked Verdict = "fits_single_host_blocked"
	// VerdictFitsShardedOnly: no single host fits, but pool.PlanMLX found a
	// layout across peers. Multi-host execution is not available, so it is not
	// runnable.
	VerdictFitsShardedOnly Verdict = "fits_sharded_only"
	// VerdictDoesNotFit: neither a single host nor any peer layout fits.
	VerdictDoesNotFit Verdict = "does_not_fit"
)

// Severity of a Blocker.
type Severity string

const (
	SeverityBlocker Severity = "blocker" // stops something the owner asked for
	SeverityWarning Severity = "warning" // degrades or limits it
	SeverityInfo    Severity = "info"    // a fact the owner should know
)

// Runtime states (RuntimeReport.State).
const (
	RuntimeHealthy             = "healthy"              // probe ran, platform and package pins match
	RuntimeNotConfigured       = "not_configured"       // no owner policy: runtime was never provisioned
	RuntimeIntegrityMismatch   = "integrity_mismatch"   // interpreter or runtime tree differs from the owner's pins
	RuntimeProbeFailed         = "probe_failed"         // probe did not run or returned garbage
	RuntimeUnsupportedPlatform = "unsupported_platform" // not Darwin/arm64
	RuntimeDependenciesMissing = "dependencies_missing" // mlx / mlx-lm / transformers not installed
	RuntimeVersionMismatch     = "version_mismatch"     // installed versions differ from the runtime's pins
	RuntimeUnverified          = "unverified"           // another Mac: cannot be probed remotely
)

// Report is the whole preflight output (`nexal inference preflight --json`).
type Report struct {
	SchemaVersion int    `json:"schemaVersion"`
	GeneratedAt   string `json:"generatedAt"`
	// Headline is one plain sentence that answers "what can I run?".
	Headline       string          `json:"headline"`
	Machines       []MachineReport `json:"machines"`
	Links          []LinkReport    `json:"links"`
	Models         []ModelVerdict  `json:"models"`
	Recommendation Recommendation  `json:"recommendation"`
	Sharing        SharingReport   `json:"sharing"`
	Blockers       []Blocker       `json:"blockers"`
	// Gaps lists data this check could not obtain. Nothing in the report fills
	// a gap with a guess unless the field is labelled as an assumption.
	Gaps        []string `json:"gaps"`
	Assumptions []string `json:"assumptions"`
	// CatalogNote states that memory figures are estimates.
	CatalogNote string `json:"catalogNote"`
	// Surface says plainly which user surfaces exist today.
	Surface string `json:"surface"`
}

// RuntimeReport is the MLX runtime's state on one Mac.
type RuntimeReport struct {
	State        string `json:"state"`
	Detail       string `json:"detail"`
	Version      string `json:"version,omitempty"`
	MLXVersion   string `json:"mlxVersion,omitempty"`
	MLXLMVersion string `json:"mlxLmVersion,omitempty"`
	// DistributedExecution is the probe's own "distributed_execution" field.
	DistributedExecution string `json:"distributedExecution,omitempty"`
	// Verified is true only when this process actually probed the runtime.
	Verified bool `json:"verified"`
}

// MachineReport is one Mac (this one or an online peer).
type MachineReport struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	IsSelf bool   `json:"isSelf"`
	Online bool   `json:"online"`
	Chip   string `json:"chip,omitempty"`
	OS     string `json:"os,omitempty"`

	TotalMemoryBytes     uint64 `json:"totalMemoryBytes"`
	AvailableMemoryBytes uint64 `json:"availableMemoryBytes"`
	// ApprovedMemoryBytes is the owner's workload cap as the admission layer
	// enforces it. On another Mac it is an assumption (AdmissionSource says so).
	ApprovedMemoryBytes uint64 `json:"approvedMemoryBytes"`
	OwnerReserveBytes   uint64 `json:"ownerReserveBytes"`
	// HeadroomBytes is available memory minus the owner reserve, the planner's
	// MeasuredHeadroom.
	HeadroomBytes uint64 `json:"headroomBytes"`
	// UsableBytes is what pool.PlanMLX can place on this Mac:
	// min(approved, headroom).
	UsableBytes     uint64 `json:"usableBytes"`
	AdmissionSource string `json:"admissionSource"` // local-policy | assumed-peer-defaults | unavailable

	DiskFreeBytes   uint64 `json:"diskFreeBytes"`
	DiskKnown       bool   `json:"diskKnown"`
	DiskReserveByte uint64 `json:"diskReserveBytes"`

	Runtime  RuntimeReport `json:"runtime"`
	Eligible bool          `json:"eligible"`
	// Reasons explains why a Mac is not eligible, in plain language.
	Reasons []string `json:"reasons"`
}

// LinkReport is the link from this Mac to one peer. Measured fields are
// pointers: an unmeasured value is absent, never zero.
type LinkReport struct {
	PeerID            string   `json:"peerId"`
	PeerName          string   `json:"peerName"`
	Online            bool     `json:"online"`
	Path              string   `json:"path"` // direct | relay | cloud | unknown
	PathLabel         string   `json:"pathLabel,omitempty"`
	DirectVia         string   `json:"directVia,omitempty"` // lan | nat
	LatencyMS         *float64 `json:"latencyMs,omitempty"`
	BandwidthMbps     *float64 `json:"bandwidthMbps,omitempty"`
	PacketLossPercent *float64 `json:"packetLossPercent,omitempty"`
	PQ                string   `json:"pq"`
	PQReason          string   `json:"pqReason,omitempty"`
	PathFlapsLastHour int      `json:"pathFlapsLastHour"`
	Unstable          bool     `json:"unstable"`
	// Transport is the data-plane mechanism. Nothing reports RDMA or Thunderbolt
	// today, so this is "tcp-over-mesh" with RDMA "unknown"; see macos
	// TransportCapability.swift ("unknown is the normal state").
	Transport string `json:"transport"`
	RDMA      string `json:"rdma"`
	// Quality is good | fair | poor | unusable (preflight heuristic, see link.go).
	Quality string   `json:"quality"`
	Notes   []string `json:"notes"`
}

// RankLayout is one rank of a planned layout.
type RankLayout struct {
	Rank          int    `json:"rank"`
	MachineID     string `json:"machineId"`
	MachineName   string `json:"machineName"`
	RequiredBytes int64  `json:"requiredBytes"`
	UsableBytes   int64  `json:"usableBytes"`
}

// ModelVerdict is the result for one catalog entry.
type ModelVerdict struct {
	ID             string  `json:"id"`
	DisplayName    string  `json:"displayName"`
	ParamsBillions float64 `json:"paramsBillions"`
	Quantization   string  `json:"quantization"`
	Verdict        Verdict `json:"verdict"`
	// Summary is the plain-language line for the report.
	Summary string `json:"summary"`
	// RequiredBytesSingleHost is the estimated total for one host (an estimate).
	RequiredBytesSingleHost int64  `json:"requiredBytesSingleHost"`
	HostID                  string `json:"hostId,omitempty"`
	HostName                string `json:"hostName,omitempty"`
	// Layout is set for fits_sharded_only (planned, not executable).
	Layout []RankLayout `json:"layout,omitempty"`
	// LayoutLinksOK is false when a peer in the layout has a poor link.
	LayoutLinksOK *bool `json:"layoutLinksOk,omitempty"`
	// FitsIfCapRaised: the model would fit on some host if the owner memory cap
	// were not limiting. Only set when it does not fit as configured.
	FitsIfCapRaised bool     `json:"fitsIfCapRaised,omitempty"`
	Reasons         []string `json:"reasons"`
	Installable     bool     `json:"installable"`
	// DownloadBytes is the sum of the pinned file sizes; omitted when unpinned.
	DownloadBytes  int64  `json:"downloadBytes,omitempty"`
	InstallBlocked string `json:"installBlockedReason,omitempty"`
	// ModelProvisioning: weights are not detected by preflight.
	ModelProvisioning string `json:"modelProvisioning"`
}

// Recommendation is the "most advanced model your hardware supports".
type Recommendation struct {
	// ModelID is empty when nothing in the catalog fits.
	ModelID     string `json:"modelId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	// RunnableNow is true only for a runs_now verdict.
	RunnableNow bool   `json:"runnableNow"`
	HostName    string `json:"hostName,omitempty"`
	Reason      string `json:"reason"`
	// HowToUse is short, plain text for the recommended model using only
	// commands and surfaces that exist today.
	HowToUse []string `json:"howToUse"`
}

// SharingReport is the strictly honest answer to "would sharing work?".
type SharingReport struct {
	SingleHostExecution string `json:"singleHostExecution"` // available | blocked
	MultiHostExecution  string `json:"multiHostExecution"`  // unavailable
	Statement           string `json:"statement"`
	// Evidence names the code paths that were checked.
	Evidence []string `json:"evidence"`
}

// Blocker is one thing in the way, with a concrete fix.
type Blocker struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Scope    string   `json:"scope"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Fix      string   `json:"fix"`
	// FixCommand is a command that exists today, when the fix is a command.
	FixCommand string `json:"fixCommand,omitempty"`
}
