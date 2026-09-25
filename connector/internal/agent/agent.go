// Package agent enforces owner-first single-job resource and lease admission.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/contribution"
	"nexal/connector/internal/mesh"
	"nexal/connector/internal/throttle"
	"nexal/connector/internal/wol"
)

type Record struct {
	Fingerprint    string         `json:"fingerprint"`
	State          string         `json:"state"`
	Result         *client.Result `json:"result,omitempty"`
	UsageSeconds   float64        `json:"usageSeconds,omitempty"`
	LeaseExpiresAt time.Time      `json:"leaseExpiresAt"`
}
type Status struct {
	ManualAcceptanceSupported  bool                  `json:"manualAcceptanceSupported"`
	AcceptJobsUntil            string                `json:"acceptJobsUntil,omitempty"`
	OwnerActivityOverride      bool                  `json:"ownerActivityOverride"`
	ExecutionBlocker           string                `json:"executionBlocker,omitempty"`
	Version                    string                `json:"version"`
	HostID                     string                `json:"hostId"`
	Mode                       string                `json:"mode"`
	Transport                  string                `json:"transport"`
	Paused                     bool                  `json:"paused"`
	MarketplaceEnabled         bool                  `json:"marketplaceEnabled"`
	ProductionDispatchVerified bool                  `json:"productionDispatchVerified"`
	Telemetry                  Telemetry             `json:"telemetry"`
	ActiveAttempt              string                `json:"activeAttempt,omitempty"`
	LastOutcome                string                `json:"lastOutcome,omitempty"`
	PQ                         client.PQ             `json:"pq"`
	CoordinatorHealthy         bool                  `json:"coordinatorHealthy"`
	ResourcePolicy             config.ResourcePolicy `json:"resourcePolicy"`
	UploadThrottle             UploadThrottle        `json:"uploadThrottle"`
	// Contribution is HARDENING-PLAN §36.4's conditional-contribution verdict.
	// Paused above and Contribution.withholding below are DIFFERENT facts and are
	// reported separately on purpose: the first is the owner's persisted decision,
	// the second is automatic, transient and never written to disk.
	Contribution ContributionStatus `json:"contribution"`
	Mesh         mesh.Status        `json:"mesh"`
	// Presence and Wake are additive (see presence.go). Presence is the
	// coordinator's live online set; Wake is this Mac's own Wake-on-LAN facts.
	Presence PresenceStatus `json:"presence"`
	Wake     WakeStatus     `json:"wake"`

	// CredentialRejected: the coordinator no longer accepts this Mac's credential;
	// the owner must leave and pair again. Additive; false when unknown.
	CredentialRejected bool `json:"credentialRejected"`
}

// UploadThrottle is the §26 "always see why" view of the upload dimension: the
// number in force, where it came from, and whether anything is actually obeying
// it. Enforced is hardcoded false and stays false until a bulk upload path
// exists (HARDENING-PLAN §21 Step 0); reporting a ceiling as enforced when
// nothing uploads bulk data would be the fabrication this feature must avoid.
type UploadThrottle struct {
	Mode                    string `json:"mode"`
	EffectiveBytesPerSecond uint64 `json:"effectiveBytesPerSecond"`
	Source                  string `json:"source"`
	MeteredStatus           string `json:"meteredStatus"`
	Enforced                bool   `json:"enforced"`
	Reason                  string `json:"reason"`
}
type Agent struct {
	mu            sync.Mutex
	cfg           config.Config
	path          string
	api           client.API
	probe         Probe
	telemetry     Telemetry
	telemetryAt   time.Time
	pq            client.PQ
	active        string
	cancel        context.CancelFunc
	lastOutcome   string
	records       map[string]Record
	lastHeartbeat time.Time

	// credentialRejectedAt is set when the coordinator answers a heartbeat with 401:
	// this host's credential was revoked or expired (removed from the phone, or
	// released on the server). Retrying every 15 s cannot fix that, so heartbeats
	// back off to credentialRetryInterval and status tells the app to re-pair.
	credentialRejectedAt time.Time

	heartbeatWake chan struct{}
	// A consent change fences asynchronous observations started under the old
	// policy. Per-operation sequence numbers also prevent out-of-order results.
	stateGeneration     uint64
	probeGeneration     uint64
	heartbeatGeneration uint64
	// Only explicit development pull can execute. Production dispatch has no
	// bypass flag and remains closed even if a tunnel reports a QUIC connection.
	devPull           bool
	manualUntil       time.Time
	manualEnabledPull bool
	// discovery is nil unless WithDiscovery was given, which is the default: a
	// connector with no discovery block in its config announces nothing and
	// polls nothing. peerView is the last merged candidate view, guarded by mu.
	discovery *DiscoveryOptions
	peerView  PeerView
	// metered reports whether the current network path is metered. It is nil in
	// every shipped configuration because the only reliable macOS signal is
	// NWPathMonitor's isExpensive/isConstrained, which is Swift and cannot be
	// built here; nil means "unknown", which deliberately applies no metered
	// ceiling rather than guessing. Set by WithMeteredSource once that bridge
	// exists. Read under mu.
	metered throttle.MeteredSource
	// conditionSource and conditions are the §36.4 power/thermal/disk dimensions.
	// conditions is the last observation and is read under mu; it is memory-only,
	// because automatic withholding must never become persisted state that could
	// outlive the condition or be confused with cfg.Paused.
	conditionSource contribution.Source
	conditions      contribution.Signals
	// bridge is the single Swift→Go signal carrier (metered + authoritative
	// thermal). Nil in every shipped configuration today; see
	// contribution.PlatformBridge for why one interface carries both.
	bridge contribution.PlatformBridge
	// logger is set by New and never written again, so every goroutine below can
	// read it without a.mu. Records carry attempt identifiers, states, durations
	// and this package's fixed error strings; never tokens, admin credentials,
	// ciphertext, key material or coordinator URLs. The client package already
	// reduces transport failures to fixed messages for the same reason.
	logger       *slog.Logger
	meshProvider mesh.Provider
	// presence is the live online-set stream; nil unless WithPresence. It has
	// its own lock and never takes a.mu, so reading it under a.mu is safe.
	presence PresenceSource
	// wakeReporter publishes wake facts; nil unless WithWakeInfo. wakeFacts is
	// the local collector (wol.CollectLocal), replaceable by tests. wake is the
	// last collected view for status, guarded by mu.
	wakeReporter WakeInfoReporter
	wakeFacts    func(context.Context) wol.Facts
	wake         WakeStatus
	// wakeInfoEvery overrides both wake-info waits when nonzero. Tests only;
	// production leaves it zero and gets wakeInfoInterval/RetryInterval.
	wakeInfoEvery time.Duration
}

// Option configures optional Agent behaviour. Options exist so observability can
// be added without changing New's signature for every caller and test.
type Option func(*Agent)

// WithLogger installs a structured logger. Without it an Agent discards its logs,
// which keeps tests and library callers silent by default.
func WithLogger(l *slog.Logger) Option {
	return func(a *Agent) {
		if l != nil {
			a.logger = l
		}
	}
}

// WithMeteredSource installs an OS path-cost signal. Without it the connector
// reports metered status as unknown and applies no metered ceiling: guessing
// metered would crawl for every owner on a normal link, and guessing unmetered
// could bill someone on a hotspot. Neither guess is acceptable, so neither is made.
func WithMeteredSource(m throttle.MeteredSource) Option {
	return func(a *Agent) { a.metered = m }
}

// WithMeshProvider installs a read-only status adapter. It cannot install,
// launch, or silently authorize privileged networking software.
func WithMeshProvider(p mesh.Provider) Option {
	return func(a *Agent) {
		if p != nil {
			a.meshProvider = p
		}
	}
}

func New(c config.Config, path string, api client.API, probe Probe, devPull bool, opts ...Option) (*Agent, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if devPull && !c.Development {
		return nil, errors.New("outbound pull execution is development-only; verified production tunnel dispatch is not integrated")
	}
	if probe == nil {
		return nil, errors.New("telemetry probe required")
	}
	a := &Agent{cfg: c, path: path, api: api, probe: probe, devPull: devPull, records: map[string]Record{}, telemetry: Telemetry{OwnerActive: true}, heartbeatWake: make(chan struct{}, 1), meshProvider: mesh.UnavailableProvider{},
		logger: slog.New(slog.DiscardHandler), wakeFacts: wol.CollectLocal,
		wake: WakeStatus{MACs: []string{}, WakeForNetwork: wol.WakeUnknown}}
	for _, opt := range opts {
		opt(a)
	}
	b, err := config.ReadPrivate(a.journalPath(), 8<<20)
	if err == nil {
		if err = json.Unmarshal(b, &a.records); err != nil || a.records == nil {
			return nil, errors.New("attempt journal is invalid; refusing replay-unsafe startup")
		}
		if len(a.records) > 10000 {
			return nil, errors.New("attempt journal is full")
		}
		for id, r := range a.records {
			// A process restart fences previously running work instead of
			// silently rerunning it; the coordinator may issue a new attempt.
			if r.State == "running" {
				r.State = "interrupted"
				a.records[id] = r
				// Unsettled work that a restart fenced. The coordinator may
				// reissue it; nothing here re-runs it, so say so once.
				a.logger.Warn("fenced attempt interrupted by restart", "attempt", id)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return a, nil
}
func (a *Agent) journalPath() string { return filepath.Join(filepath.Dir(a.path), "attempts.json") }
func (a *Agent) persistLocked() error {
	b, err := json.Marshal(a.records)
	if err != nil {
		return err
	}
	return config.AtomicPrivate(a.journalPath(), b)
}
func (a *Agent) Snapshot() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	mode := "production (dispatch gated)"
	if a.cfg.Development {
		mode = "DEVELOPMENT PREVIEW — no external spending"
	}
	transport := "heartbeat-only; production tunnel dispatch unavailable"
	if a.devPull {
		transport = "outbound HTTP(S) private pull prototype — NOT protected by incoming PQ tunnel"
	}
	until := ""
	if a.manualActiveLocked() {
		until = a.manualUntil.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	blocker := ""
	if err := a.admitLocked(true); err != nil {
		blocker = err.Error()
	}
	return Status{ManualAcceptanceSupported: a.cfg.Development, AcceptJobsUntil: until,
		UploadThrottle: a.uploadThrottleLocked(), Contribution: a.contributionStatusLocked(),
		OwnerActivityOverride: a.manualActiveLocked(), ExecutionBlocker: blocker,
		Version: config.Version, HostID: a.cfg.HostID, Mode: mode, Transport: transport, Paused: a.cfg.Paused,
		Telemetry: a.telemetry, ActiveAttempt: a.active, LastOutcome: a.lastOutcome, PQ: a.pq,
		ResourcePolicy: a.cfg.ResourcePolicy(), Mesh: mesh.SanitizeSnapshot(a.meshProvider.Snapshot()),
		Presence: a.presenceStatusLocked(), Wake: a.wakeStatusLocked(),
		CoordinatorHealthy: !a.lastHeartbeat.IsZero() && time.Since(a.lastHeartbeat) < 30*time.Second,
		CredentialRejected: !a.credentialRejectedAt.IsZero()}
}

// AcceptJobsNow is explicit, local owner consent for ten minutes of zero-cost
// private development work while active. Real resource/lease checks still apply.
// The permission is not persisted and cannot enable production or marketplace work.
func (a *Agent) AcceptJobsNow() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.Development {
		return errors.New("manual acceptance is development-only")
	}
	if a.manualActiveLocked() {
		return nil
	} // retries do not extend consent
	if a.active != "" {
		return errors.New("wait for the active attempt or pause it first")
	}
	next := a.cfg
	next.Paused = false
	if err := config.Save(a.path, next); err != nil {
		return err
	}
	a.invalidateConsentLocked()
	a.cfg = next
	a.manualEnabledPull = !a.devPull
	a.devPull = true
	a.manualUntil = time.Now().Add(10 * time.Minute)
	a.wakeHeartbeatLocked()
	return nil
}

func (a *Agent) manualActiveLocked() bool {
	return a.cfg.Development && !a.cfg.Paused && time.Now().Before(a.manualUntil)
}

// SetResourcePolicy persists consent before making it effective. Any change
// cancels running work and requires a fresh probe before new admission. Failure
// to persist still stops work, but does not apply an unsaved policy or resume.
func (a *Agent) SetResourcePolicy(p config.ResourcePolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	// Normalize before comparing, so an older client that omits uploadMode is
	// recognised as asking for the default rather than as requesting a change.
	p.UploadMode = config.NormalizeUploadMode(p.UploadMode)
	a.mu.Lock()
	defer a.mu.Unlock()
	// The measured rate is derived, not consented: a policy update carries the
	// owner's mode and manual limit, and must not erase what auto has learned
	// about this link. DecodeResourcePolicy already discards any client value.
	p.MeasuredUploadBytesPerSecond = a.cfg.MeasuredUploadBytesPerSecond
	if p == a.cfg.ResourcePolicy() {
		return nil
	}
	a.invalidateConsentLocked()
	next := a.cfg.WithResourcePolicy(p)
	if err := config.Save(a.path, next); err != nil {
		// Persistence failure must not silently permit more execution under
		// assumptions the owner intended to change.
		a.cfg.Paused = true
		return err
	}
	a.cfg = next
	return nil
}
func (a *Agent) SetPaused(paused bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.cfg
	next.Paused = paused
	if a.cfg.Paused != paused {
		a.invalidateConsentLocked()
	}
	// Owner stop takes effect even if persistence fails; resume does not.
	if paused {
		a.cfg.Paused = true
		if a.cancel != nil {
			a.cancel()
		}
		a.wakeHeartbeatLocked()
	}
	if err := config.Save(a.path, next); err != nil {
		return err
	}
	a.cfg = next
	a.wakeHeartbeatLocked()
	return nil
}
func (a *Agent) invalidateConsentLocked() {
	a.manualUntil = time.Time{}
	if a.manualEnabledPull {
		a.devPull = false
		a.manualEnabledPull = false
	}
	a.stateGeneration++
	a.lastHeartbeat = time.Time{}
	a.telemetryAt = time.Time{}
	a.telemetry.Known = false
	a.telemetry.OwnerActive = true
	a.telemetry.AvailableMemoryBytes = 0
	// a.conditions is deliberately NOT cleared. A consent change fences
	// observations that could wrongly PERMIT work; discarding the §36.4 conditions
	// would do the opposite — unknown does not withhold, so clearing them would
	// let a policy edit resume contribution on a laptop that is still on battery.
	// They age out through contribution.StaleAfter instead.
	if a.cancel != nil {
		a.cancel()
	}
	a.wakeHeartbeatLocked()
}
func (a *Agent) wakeHeartbeatLocked() {
	select {
	case a.heartbeatWake <- struct{}{}:
	default:
	}
}
func (a *Agent) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Also fence a pending pull; cancelling between Next and Execute must not
	// let a response obtained under the old owner decision start new work.
	a.invalidateConsentLocked()
}
func (a *Agent) SetPQ(p client.PQ) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Self-reported diagnostics never satisfy deployment verification.
	p.Verified = false
	a.pq = p
}
func (a *Agent) Refresh(ctx context.Context) {
	a.mu.Lock()
	generation := a.stateGeneration
	a.probeGeneration++
	probe := a.probeGeneration
	a.mu.Unlock()
	started := time.Now()
	t := a.probe(ctx)
	if !t.Known {
		t.OwnerActive = true
		t.AvailableMemoryBytes = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil || generation != a.stateGeneration || probe != a.probeGeneration {
		return
	}
	// The live policy, not a threshold captured when the process started,
	// decides admission. A probe may additionally force OwnerActive true.
	t.OwnerActive = t.OwnerActive || t.IdleSeconds < a.cfg.IdleSeconds
	if a.telemetry.OwnerActive != t.OwnerActive || a.telemetry.Known != t.Known {
		a.wakeHeartbeatLocked()
	}
	a.telemetry = t
	// Freshness starts when sampling started, not when a stalled probe returns.
	a.telemetryAt = started
	if a.admitLocked(false) != nil && a.cancel != nil {
		a.cancel()
	}
}
func (a *Agent) admitLocked(checkBusy bool) error {
	if !a.devPull || !a.cfg.Development {
		return errors.New("production job execution gated pending verified tunnel dispatch")
	}
	if a.manualEnabledPull && !a.manualActiveLocked() {
		return errors.New("manual acceptance expired; click Accept jobs now to renew")
	}
	if a.cfg.Paused {
		return errors.New("host paused by owner")
	}
	if a.cfg.Enrollment != nil {
		status := mesh.SanitizeSnapshot(a.meshProvider.Snapshot())
		if !status.StrictPQReady() {
			return errors.New("quantum-safe peer verification required before compute or sharing")
		}
	}
	// §36.4: default-on contribution must be CONDITIONAL. This is checked AFTER
	// the owner's pause and as a separate clause, so the two never merge: the
	// owner's flag is persisted consent, this is a transient machine condition,
	// and each must survive the other. Unknown conditions do not withhold, so a
	// Mac whose thermal state cannot be read keeps working with the reason
	// visible in status rather than silently going dark.
	if d := a.contributionLocked(); d.Withholding {
		return errors.New("withholding contribution — " + d.Summary)
	}
	if a.lastHeartbeat.IsZero() || time.Since(a.lastHeartbeat) > 30*time.Second {
		return errors.New("fresh authenticated coordinator heartbeat required")
	}
	if !a.telemetry.Known || time.Since(a.telemetryAt) > 10*time.Second {
		return errors.New("fresh resource and owner telemetry required")
	}
	if a.telemetry.OwnerActive && !a.manualActiveLocked() {
		return errors.New("owner priority blocks execution")
	}
	if a.telemetry.AvailableMemoryBytes > a.telemetry.TotalMemoryBytes ||
		a.telemetry.AvailableMemoryBytes < a.cfg.ReserveMemoryBytes ||
		a.telemetry.AvailableMemoryBytes-a.cfg.ReserveMemoryBytes < WorkloadMemoryBytes ||
		a.cfg.MemoryLimitBytes < WorkloadMemoryBytes {
		return errors.New("insufficient approved memory headroom")
	}
	if checkBusy && a.active != "" {
		return errors.New("host already has an active attempt")
	}
	return nil
}
func fingerprint(at client.Attempt) string {
	// Lease can renew, but the immutable identity/workload cannot change.
	at.LeaseExpiresAt = time.Time{}
	b, _ := json.Marshal(at)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (a *Agent) finish(id, state string, r *client.Result, usage float64, expiry time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := a.records[id]
	rec.State = state
	rec.Result = r
	rec.UsageSeconds = usage
	rec.LeaseExpiresAt = expiry
	a.records[id] = rec
	a.lastOutcome = state
	return a.persistLocked()
}

// Execute validates and journals before starting, and rejects stale/conflicting
// duplicate attempts. No caller can enable a production path.
func (a *Agent) Execute(ctx context.Context, at client.Attempt) error {
	return a.execute(ctx, at, nil)
}

func (a *Agent) execute(ctx context.Context, at client.Attempt, generation *uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateAttempt(at, a.Snapshot().HostID, time.Now()); err != nil {
		return err
	}
	a.mu.Lock()
	if a.api == nil || (generation != nil && *generation != a.stateGeneration) {
		a.mu.Unlock()
		return errors.New("attempt admission invalidated")
	}
	if old, ok := a.records[at.ID]; ok {
		if old.Fingerprint != fingerprint(at) {
			a.mu.Unlock()
			return errors.New("conflicting duplicate attempt")
		}
		a.mu.Unlock()
		// Completed/running/interrupted attempts are idempotent no-ops.
		// Unknown outcome is deliberately not automatically re-executed.
		return nil
	}
	if err := a.admitLocked(true); err != nil {
		a.mu.Unlock()
		return err
	}
	if a.manualActiveLocked() && (at.Execution != "private" || at.MaxCostCents != 0) {
		a.mu.Unlock()
		return errors.New("manual acceptance requires an explicitly private zero-cost attempt")
	}
	if len(a.records) >= 10000 {
		a.mu.Unlock()
		return errors.New("attempt journal full; refusing replay-unsafe eviction")
	}
	workCtx, cancel := context.WithCancel(ctx)
	a.active = at.ID
	a.cancel = cancel
	a.records[at.ID] = Record{Fingerprint: fingerprint(at), State: "running", LeaseExpiresAt: at.LeaseExpiresAt}
	if err := a.persistLocked(); err != nil {
		a.active = ""
		a.cancel = nil
		delete(a.records, at.ID)
		a.mu.Unlock()
		cancel()
		return err
	}
	a.mu.Unlock()
	defer func() { cancel(); a.mu.Lock(); a.active = ""; a.cancel = nil; a.mu.Unlock() }()
	var leaseMu sync.Mutex
	expiry := at.LeaseExpiresAt
	deadline := func() time.Time { leaseMu.Lock(); defer leaseMu.Unlock(); return expiry }
	// Reclaim runs independently of probes and renewal I/O: a stalled monitor
	// cannot keep work alive after its resource/coordinator observations expire.
	// Keep watching through completion I/O as well as computation.
	var watchWG, renewWG sync.WaitGroup
	watchWG.Add(1)
	defer func() { cancel(); watchWG.Wait() }()
	go func() {
		defer watchWG.Done()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-tick.C:
				a.mu.Lock()
				allowed := a.admitLocked(false) == nil
				a.mu.Unlock()
				if !allowed || !time.Now().Before(deadline()) {
					cancel()
					return
				}
			}
		}
	}()
	renewCtx, stopRenewal := context.WithCancel(workCtx)
	defer stopRenewal()
	renewWG.Add(1)
	go func() {
		defer renewWG.Done()
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-tick.C:
				reqCtx, stop := boundedRequest(renewCtx, deadline())
				r, err := a.api.Renew(reqCtx, at.ID)
				stop()
				if renewCtx.Err() != nil {
					return
				}
				if err != nil || r.CancelRequested || !r.OK {
					cancel()
					return
				}
				now := time.Now()
				leaseMu.Lock()
				if !now.Before(expiry) || !r.LeaseExpiresAt.After(now) || r.LeaseExpiresAt.After(now.Add(2*time.Minute)) || r.LeaseExpiresAt.Before(expiry) {
					leaseMu.Unlock()
					cancel()
					return
				}
				expiry = r.LeaseExpiresAt
				leaseMu.Unlock()
			}
		}
	}()
	start := time.Now()
	result, err := MonteCarlo(workCtx, at, deadline)
	seconds := time.Since(start).Seconds()
	// Cancelling only renewal I/O prevents a finished workload waiting for a
	// blocked Renew until lease expiry, without cancelling result submission.
	stopRenewal()
	renewWG.Wait()
	a.mu.Lock()
	allowed := a.admitLocked(false) == nil
	a.mu.Unlock()
	if err != nil || workCtx.Err() != nil || !allowed || !time.Now().Before(deadline()) {
		// Which of the four reasons applied is the whole diagnostic value here:
		// owner activity, resource pressure, a cancelled context and an expired
		// lease are very different operational problems.
		a.logger.Warn("attempt abandoned", "attempt", at.ID, "seconds", seconds,
			"workload_error", errorText(err), "context_error", errorText(workCtx.Err()),
			"admitted", allowed, "lease_expired", !time.Now().Before(deadline()))
		if ferr := a.finish(at.ID, "cancelled-or-lease-expired", nil, seconds, deadline()); ferr != nil {
			a.logger.Error("attempt journal write failed", "attempt", at.ID,
				"state", "cancelled-or-lease-expired", "error", errorText(ferr))
		}
		if err != nil {
			return err
		}
		return errors.New("attempt cancelled or lease expired")
	}
	if err := a.finish(at.ID, "result-pending", &result, seconds, deadline()); err != nil {
		return err
	}
	// Completion retries reuse exactly the same attempt/result; they never
	// rerun computation. Coordinator settlement remains authoritative.
	for retries := 0; retries < 3; retries++ {
		if workCtx.Err() != nil || !time.Now().Before(deadline()) {
			break
		}
		reqCtx, stop := boundedRequest(workCtx, deadline())
		accepted, callErr := a.api.Complete(reqCtx, at.ID, result, seconds)
		stop()
		if callErr != nil {
			// Retried below; a single failure is routine, three are not.
			a.logger.Debug("completion submission failed", "attempt", at.ID,
				"retry", retries, "error", errorText(callErr))
		}
		if callErr == nil {
			state := "completion-rejected"
			if accepted {
				state = "completed"
			}
			a.logger.Info("attempt settled", "attempt", at.ID, "state", state,
				"seconds", seconds, "retries", retries)
			if err := a.finish(at.ID, state, &result, seconds, deadline()); err != nil {
				return err
			}
			if !accepted {
				return errors.New("coordinator fenced completion")
			}
			return nil
		}
		select {
		case <-workCtx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	// The work is done and paid for locally but the coordinator never confirmed
	// settlement. Nothing re-executes automatically, so this must be visible.
	a.logger.Error("completion unconfirmed after retries", "attempt", at.ID,
		"seconds", seconds, "retries", 3)
	if ferr := a.finish(at.ID, "completion-unconfirmed", &result, seconds, deadline()); ferr != nil {
		a.logger.Error("attempt journal write failed", "attempt", at.ID,
			"state", "completion-unconfirmed", "error", errorText(ferr))
	}
	return errors.New("completion unconfirmed; no automatic re-execution")
}

func boundedRequest(ctx context.Context, lease time.Time) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(10 * time.Second)
	if !lease.IsZero() && lease.Before(deadline) {
		deadline = lease
	}
	return context.WithDeadline(ctx, deadline)
}

// errorText renders an error for a log record. Errors reaching these sites are
// this package's own fixed strings or the client package's deliberately sanitized
// transport messages, which carry no URL, credential or response text; funnelling
// them through one place keeps that reviewable and bounds the field so a long
// unexpected error cannot bloat a log line.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 256 {
		s = strings.ToValidUTF8(s[:256], "") + " …(truncated)"
	}
	return s
}

// heartbeat runs one host heartbeat and records why it failed. A failed heartbeat
// clears lastHeartbeat and cancels running work, so silence here used to make an
// abandoned attempt look spontaneous. trigger distinguishes the scheduled beat
// from the consent-change beat, which is the difference between a network problem
// and an owner action.
// credentialRetryInterval is how often a host whose credential was rejected still
// checks in: often enough to recover if the rejection was transient, rare enough
// not to fill the coordinator's logs with 401s from a Mac nobody re-paired.
const credentialRetryInterval = 10 * time.Minute

func (a *Agent) heartbeat(ctx context.Context, trigger string) {
	a.mu.Lock()
	rejected := a.credentialRejectedAt
	a.mu.Unlock()
	if !rejected.IsZero() && trigger == "interval" && time.Since(rejected) < credentialRetryInterval {
		return
	}
	err := a.hostHeartbeat(ctx)
	var status *client.StatusError
	a.mu.Lock()
	switch {
	case err == nil:
		a.credentialRejectedAt = time.Time{}
	case errors.As(err, &status) && status.Status == http.StatusUnauthorized:
		a.credentialRejectedAt = time.Now()
	}
	a.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			// Shutdown, not a fault.
			a.logger.Debug("host heartbeat abandoned during shutdown", "trigger", trigger)
			return
		}
		if status != nil && status.Status == http.StatusUnauthorized {
			a.logger.Warn("coordinator no longer accepts this host's credential; leave and pair this Mac again",
				"retryIn", credentialRetryInterval.String())
			return
		}
		a.logger.Warn("host heartbeat failed", "trigger", trigger, "error", errorText(err))
	}
}

func (a *Agent) hostHeartbeat(ctx context.Context) error {
	ctx, stop := boundedRequest(ctx, time.Time{})
	defer stop()
	a.mu.Lock()
	generation := a.stateGeneration
	a.heartbeatGeneration++
	heartbeat := a.heartbeatGeneration
	known := a.telemetry.Known && time.Since(a.telemetryAt) <= 10*time.Second
	// Automatic withholding rides the existing OwnerActive flag, which is the
	// coordinator's only "do not send me work" input. That is what makes §36.4
	// real rather than display-only: a host on battery stops being offered
	// attempts instead of merely refusing them locally. It is also a CONFLICT
	// worth recording — the wire protocol has no field for "withholding, and
	// why", so the reason is visible locally (status/UI per §26) but the
	// coordinator cannot distinguish a busy owner from a hot machine.
	withholding := a.contributionLocked().Withholding
	h := client.Heartbeat{OwnerActive: a.cfg.Paused || !known || a.telemetry.OwnerActive || withholding,
		PQ: a.pq, Version: config.Version}
	if a.manualActiveLocked() {
		h.AcceptJobsUntil = a.manualUntil.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if known {
		h.AvailableMemoryBytes = a.telemetry.AvailableMemoryBytes
	}
	hostID := a.cfg.HostID
	enrolledMesh := a.cfg.Enrollment != nil
	provider := a.meshProvider
	a.mu.Unlock()
	if gate, ok := provider.(interface{ Enrolled() bool }); ok {
		// Pairing runs in another process; trust the saved enrollment over the
		// configuration this agent loaded at start.
		enrolledMesh = gate.Enrolled()
	}
	meshStatus := mesh.SanitizeSnapshot(provider.Snapshot())
	err := a.api.Heartbeat(ctx, hostID, h)
	if err == nil && enrolledMesh {
		if reporter, ok := a.api.(interface {
			ReportTunnelStatus(context.Context, string, mesh.Status) error
		}); ok {
			err = reporter.ReportTunnelStatus(ctx, hostID, meshStatus)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if generation != a.stateGeneration || heartbeat != a.heartbeatGeneration {
		// A current heartbeat is already scheduled by the consent change.
		return err
	}
	if err != nil || ctx.Err() != nil {
		a.lastHeartbeat = time.Time{}
		if a.cancel != nil {
			a.cancel()
		}
		if err == nil {
			err = ctx.Err()
		}
		return err
	}
	a.lastHeartbeat = time.Now()
	return nil
}

func (a *Agent) Run(ctx context.Context) error {
	hostID := a.Snapshot().HostID
	if a.api == nil || !client.ValidID(hostID) {
		return errors.New("enroll this host before running")
	}
	a.Refresh(ctx)
	a.RefreshConditions(ctx)
	var wg sync.WaitGroup
	wg.Add(6)
	// Live presence and wake-info reporting are their own goroutines for the
	// same reason discovery is: a stalled WebSocket, a slow pmset or a slow
	// coordinator write must never delay a heartbeat or an attempt poll.
	// runPresence returns at once when presence is not configured.
	go func() {
		defer wg.Done()
		a.runPresence(ctx)
	}()
	go func() {
		defer wg.Done()
		a.runWakeInfo(ctx)
	}()
	// §36.4 power/thermal/disk sampling is its own goroutine on its own slower
	// cadence: it spawns processes, so it must never sit in the 2 s telemetry path,
	// and a slow pmset must not delay a heartbeat.
	go func() {
		defer wg.Done()
		t := time.NewTicker(contribution.ProbeInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.RefreshConditions(ctx)
			}
		}
	}()
	// Peer discovery is its own goroutine so a multicast join, an advertise or a
	// coordinator directory read can never delay a heartbeat or an attempt poll.
	// It returns immediately when discovery is not configured.
	go func() {
		defer wg.Done()
		a.runDiscovery(ctx)
	}()
	// Host network I/O must not block the owner/telemetry monitor.
	go func() {
		defer wg.Done()
		a.heartbeat(ctx, "startup")
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.heartbeat(ctx, "interval")
			case <-a.heartbeatWake:
				a.heartbeat(ctx, "consent-change")
			}
		}
	}()
	go func() {
		defer wg.Done()
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.mu.Lock()
				allowed := a.admitLocked(true) == nil
				generation := a.stateGeneration
				a.mu.Unlock()
				if !allowed {
					continue
				}
				reqCtx, stop := boundedRequest(ctx, time.Time{})
				at, err := a.api.Next(reqCtx, hostID)
				expired := reqCtx.Err() != nil
				stop()
				if expired || err != nil {
					// Every 2 s, so Debug: a coordinator outage would otherwise
					// bury the records that matter under thousands of lines.
					a.logger.Debug("attempt poll failed", "expired", expired, "error", errorText(err))
					continue
				}
				if at == nil {
					continue
				}
				a.logger.Info("attempt accepted", "attempt", at.ID, "job", at.JobID,
					"template", at.Template, "samples", at.Samples,
					"lease_expires_at", at.LeaseExpiresAt.UTC().Format(time.RFC3339))
				if err := a.execute(ctx, *at, &generation); err != nil {
					// execute logs the specific abandonment or settlement reason;
					// this records that the attempt ended unsuccessfully at all,
					// including the admission refusals that return before any of
					// those sites are reached.
					a.logger.Warn("attempt did not complete", "attempt", at.ID, "error", errorText(err))
				}
			}
		}
	}()
	defer func() { a.Cancel(); wg.Wait() }()
	telemetryTick := time.NewTicker(2 * time.Second)
	defer telemetryTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-telemetryTick.C:
			a.Refresh(ctx)
		}
	}
}
