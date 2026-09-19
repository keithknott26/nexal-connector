// Package agent enforces owner-first single-job resource and lease admission.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
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
}

func New(c config.Config, path string, api client.API, probe Probe, devPull bool) (*Agent, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if devPull && !c.Development {
		return nil, errors.New("outbound pull execution is development-only; verified production tunnel dispatch is not integrated")
	}
	if probe == nil {
		return nil, errors.New("telemetry probe required")
	}
	a := &Agent{cfg: c, path: path, api: api, probe: probe, devPull: devPull, records: map[string]Record{}, telemetry: Telemetry{OwnerActive: true}, heartbeatWake: make(chan struct{}, 1)}
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
		OwnerActivityOverride: a.manualActiveLocked(), ExecutionBlocker: blocker,
		Version: config.Version, HostID: a.cfg.HostID, Mode: mode, Transport: transport, Paused: a.cfg.Paused,
		Telemetry: a.telemetry, ActiveAttempt: a.active, LastOutcome: a.lastOutcome, PQ: a.pq,
		ResourcePolicy:     a.cfg.ResourcePolicy(),
		CoordinatorHealthy: !a.lastHeartbeat.IsZero() && time.Since(a.lastHeartbeat) < 30*time.Second}
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
	a.mu.Lock()
	defer a.mu.Unlock()
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
		_ = a.finish(at.ID, "cancelled-or-lease-expired", nil, seconds, deadline())
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
		if callErr == nil {
			state := "completion-rejected"
			if accepted {
				state = "completed"
			}
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
	_ = a.finish(at.ID, "completion-unconfirmed", &result, seconds, deadline())
	return errors.New("completion unconfirmed; no automatic re-execution")
}

func boundedRequest(ctx context.Context, lease time.Time) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(10 * time.Second)
	if !lease.IsZero() && lease.Before(deadline) {
		deadline = lease
	}
	return context.WithDeadline(ctx, deadline)
}

func (a *Agent) hostHeartbeat(ctx context.Context) error {
	ctx, stop := boundedRequest(ctx, time.Time{})
	defer stop()
	a.mu.Lock()
	generation := a.stateGeneration
	a.heartbeatGeneration++
	heartbeat := a.heartbeatGeneration
	known := a.telemetry.Known && time.Since(a.telemetryAt) <= 10*time.Second
	h := client.Heartbeat{OwnerActive: a.cfg.Paused || !known || a.telemetry.OwnerActive,
		PQ: a.pq, Version: config.Version}
	if a.manualActiveLocked() {
		h.AcceptJobsUntil = a.manualUntil.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if known {
		h.AvailableMemoryBytes = a.telemetry.AvailableMemoryBytes
	}
	hostID := a.cfg.HostID
	a.mu.Unlock()
	err := a.api.Heartbeat(ctx, hostID, h)
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
	var wg sync.WaitGroup
	wg.Add(2)
	// Host network I/O must not block the owner/telemetry monitor.
	go func() {
		defer wg.Done()
		_ = a.hostHeartbeat(ctx)
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = a.hostHeartbeat(ctx)
			case <-a.heartbeatWake:
				_ = a.hostHeartbeat(ctx)
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
				if expired || err != nil || at == nil {
					continue
				}
				_ = a.execute(ctx, *at, &generation)
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
