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
	Version                    string    `json:"version"`
	HostID                     string    `json:"hostId"`
	Mode                       string    `json:"mode"`
	Transport                  string    `json:"transport"`
	Paused                     bool      `json:"paused"`
	MarketplaceEnabled         bool      `json:"marketplaceEnabled"`
	ProductionDispatchVerified bool      `json:"productionDispatchVerified"`
	Telemetry                  Telemetry `json:"telemetry"`
	ActiveAttempt              string    `json:"activeAttempt,omitempty"`
	LastOutcome                string    `json:"lastOutcome,omitempty"`
	PQ                         client.PQ `json:"pq"`
	CoordinatorHealthy         bool      `json:"coordinatorHealthy"`
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
	// Only explicit development pull can execute. Production dispatch has no
	// bypass flag and remains closed even if a tunnel reports a QUIC connection.
	devPull bool
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
	return Status{Version: config.Version, HostID: a.cfg.HostID, Mode: mode, Transport: transport, Paused: a.cfg.Paused,
		Telemetry: a.telemetry, ActiveAttempt: a.active, LastOutcome: a.lastOutcome, PQ: a.pq,
		CoordinatorHealthy: !a.lastHeartbeat.IsZero() && time.Since(a.lastHeartbeat) < 30*time.Second}
}
func (a *Agent) SetPaused(paused bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.cfg
	next.Paused = paused
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
func (a *Agent) wakeHeartbeatLocked() {
	select {
	case a.heartbeatWake <- struct{}{}:
	default:
	}
}
func (a *Agent) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}
func (a *Agent) SetPQ(p client.PQ) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Self-reported diagnostics never satisfy deployment verification.
	p.Verified = false
	a.pq = p
}
func (a *Agent) Refresh(ctx context.Context) {
	t := a.probe(ctx)
	if !t.Known {
		t.OwnerActive = true
		t.AvailableMemoryBytes = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.telemetry.OwnerActive != t.OwnerActive || a.telemetry.Known != t.Known {
		a.wakeHeartbeatLocked()
	}
	a.telemetry = t
	a.telemetryAt = time.Now()
	if a.admitLocked(false) != nil && a.cancel != nil {
		a.cancel()
	}
}
func (a *Agent) admitLocked(checkBusy bool) error {
	if !a.devPull || !a.cfg.Development {
		return errors.New("production job execution gated pending verified tunnel dispatch")
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
	if a.telemetry.OwnerActive {
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
	if err := ValidateAttempt(at, a.Snapshot().HostID, time.Now()); err != nil {
		return err
	}
	a.mu.Lock()
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
	// Deadline watcher runs separately from renewal I/O, so a stalled network
	// cannot extend execution. The workload checks the same deadline itself.
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-done:
				return
			case <-tick.C:
				if !time.Now().Before(deadline()) {
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-done:
				return
			case <-tick.C:
				reqCtx, stop := context.WithDeadline(workCtx, deadline())
				r, err := a.api.Renew(reqCtx, at.ID)
				stop()
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
	close(done)
	wg.Wait()
	if err != nil || workCtx.Err() != nil || !time.Now().Before(deadline()) {
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
		reqCtx, stop := context.WithDeadline(workCtx, deadline())
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

func (a *Agent) hostHeartbeat(ctx context.Context) error {
	s := a.Snapshot()
	h := client.Heartbeat{OwnerActive: s.Paused || !s.Telemetry.Known || s.Telemetry.OwnerActive,
		AvailableMemoryBytes: s.Telemetry.AvailableMemoryBytes, PQ: s.PQ, Version: config.Version}
	if err := a.api.Heartbeat(ctx, s.HostID, h); err != nil {
		a.mu.Lock()
		a.lastHeartbeat = time.Time{}
		if a.cancel != nil {
			a.cancel()
		}
		a.mu.Unlock()
		return err
	}
	a.mu.Lock()
	a.lastHeartbeat = time.Now()
	a.mu.Unlock()
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
				a.mu.Unlock()
				if !allowed {
					continue
				}
				at, err := a.api.Next(ctx, hostID)
				if err != nil || at == nil {
					continue
				}
				_ = a.Execute(ctx, *at)
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
