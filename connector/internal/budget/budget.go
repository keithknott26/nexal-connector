// Package budget enforces HARDENING-PLAN §16's per-donor MONTHLY BANDWIDTH VOLUME,
// which is a different quantity from the bytes-per-second ceiling in internal/throttle.
//
// WHY IT EXISTS. §16: "a per-donor monthly bandwidth budget, visible in the connector UI,
// with cap-aware scheduling that throttles repair and relay work as a donor approaches
// their limit. None of this exists today." What existed was (a) throttle.Limiter, a rate
// shaper, and (b) a process-lifetime byte total. Neither bounds a month:
//   - Rate is not volume. The strictest ceiling this codebase has,
//     MeteredCeilingBytesPerSecond (64 KiB/s), sustained for 30 days is ~165 GB, so a
//     donor pinned at the metered ceiling forever still walks into a 1.2 TB ISP cap.
//   - A process-lifetime total resets on restart, so a donor who relaunches the app has
//     no cap at all. Quitting the app was a way to get more budget.
//
// THE SPLIT, and why it is this way round. The connector measures and enforces; the
// coordinator persists the window. Enforcement must be local because the connector is the
// only component in the data path — the coordinator moves no donor bytes and so cannot
// block one, and putting a network call between a chunk and its transmission would make
// every transfer depend on a remote service being up. Persistence must be remote because
// the budget has to SURVIVE A RESTART, which is the entire point, and a file the donor's
// own machine writes is state the process that just crashed is least able to trust.
//
// OFFLINE FAILS SAFE. When the coordinator is unreachable or its last answer is stale,
// this package does not fall back to "unlimited" and does not stop enforcing. It pins the
// rate at the SUSTAINED FLOOR: limit / 30 days. That rate run continuously for a month
// consumes exactly the budget and no more, so an indefinitely offline donor is still
// inside their cap — offline costs speed, never money. Before any coordinator answer has
// ever been seen, the floor is derived from ColdStartMonthlyBytes, which is deliberately
// far BELOW the coordinator's own default: a connector that cannot ask must assume the
// smaller allowance, or a coordinator outage becomes a fleet-wide budget increase.
//
// WHAT THIS PACKAGE CANNOT DO, stated because §16's harm is a real ISP bill:
//   - Bytes charged locally but not yet accepted by the coordinator are lost if the
//     process dies. The loss is bounded by one report interval (ReportInterval), not by
//     the month, which is the difference between this and the lifetime counter it
//     replaces.
//   - The coordinator cannot verify what is reported. A modified connector can under-report
//     and raise its own allowance; the party able to cheat is the party billed.
//   - throttle.Limiter refuses rates below MinBytesPerSecond (32 KiB/s), so a monthly
//     budget under ~79 GiB cannot be expressed as a sustainable RATE at all. Such a
//     budget is honoured by PAUSING when it is spent (Charge refuses), not by shaping.
//     See sustainedFloor.
package budget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"nexal/connector/internal/throttle"
)

const (
	// WindowDays mirrors BANDWIDTH_WINDOW_DAYS in the coordinator's
	// src/budget-bandwidth.ts. Duplicated across a repository boundary and therefore
	// across a deployment boundary: a connector build is not upgraded in lockstep with
	// the Worker, so both sides must hold their own copy. Only ever used here to derive
	// a CONSERVATIVE local rate, so a coordinator that moves to a longer window makes
	// this connector slower, never looser.
	WindowDays = 30
	// WindowSeconds is the divisor for every sustained-rate calculation.
	WindowSeconds = int64(WindowDays) * 86_400

	// ColdStartMonthlyBytes is the budget assumed before any coordinator answer has been
	// seen — 100 GiB, well under the coordinator's 250 GiB default, because an
	// unreachable coordinator must not be a way to obtain MORE allowance. It is above
	// the ~79 GiB that MinBytesPerSecond implies so the cold-start floor is a rate the
	// Limiter can actually hold (see sustainedFloor).
	ColdStartMonthlyBytes = uint64(100) * 1024 * 1024 * 1024

	// ReportInterval is how often accumulated bytes are pushed to the coordinator. It is
	// also the exact bound on how much accounting a crash can lose, which is why it is
	// minutes and not hours. Each report is one D1 write and one Analytics Engine data
	// point, so 15 minutes is ~96 points per donor per day: ~1,000 donors fit inside the
	// free plan's 100,000 data points/day, and that free ceiling is the thing §16 is
	// about staying inside even though the account is on Workers Paid.
	ReportInterval = 15 * time.Minute
	// StaleAfter is when a coordinator answer stops being trusted as current and the
	// offline floor takes over. Four missed reports rather than one: a single failed
	// request is a blip, and collapsing to the floor on every blip would make the
	// throttle hostage to transient DNS.
	StaleAfter = 4 * ReportInterval

	// TaperFraction is where cap-aware scheduling starts: the last quarter of the
	// budget. §16 asks for work to be throttled "as a donor approaches their limit",
	// which means before the wall, not at it — hitting zero at full speed means bulk
	// transfers stop dead mid-job.
	TaperFraction = 4
	// TaperFloorDivisor bounds the taper: at the very end of the budget the rate is
	// sustained/8, not zero. A rate of zero would be indistinguishable from "unlimited"
	// to throttle.Limiter (0 means no limit there), which is precisely the bug this
	// package must not have.
	TaperFloorDivisor = 8
)

// ErrExhausted wraps throttle.ErrBudgetExhausted so callers can match either the
// package-level sentinel or the throttle one. It is a refusal to move bytes, not a
// transport failure: a retry loop must wait for the window to roll, not retry now.
var ErrExhausted = fmt.Errorf("§16 monthly bandwidth budget: %w", throttle.ErrBudgetExhausted)

// Window is one coordinator answer: the persisted rolling-window state. Fields mirror
// the JSON the coordinator's GET/POST /api/hosts/{id}/bandwidth returns.
type Window struct {
	LimitBytes     uint64
	UsedBytes      uint64
	RemainingBytes uint64
	// SustainedBytesPerSecond is the coordinator's own limit/window arithmetic. Kept
	// separately from the locally derived floor so a disagreement is visible rather than
	// silently resolved, and the SMALLER of the two is always the one enforced.
	SustainedBytesPerSecond    uint64
	OfflineFloorBytesPerSecond uint64
	WindowDays                 int
	Exhausted                  bool
	// RetrievedAt is local time, from the injected clock — never a timestamp parsed out
	// of the response, because staleness must be measured against this machine's own
	// clock to be meaningful for "how long since I last heard anything".
	RetrievedAt time.Time
}

// Coordinator is the persistence side, an interface so every test in this package runs
// with no network at all. internal/client is deliberately not used or edited here: this
// package owns its own tiny transport (coordinator.go) and its own JSON contract.
type Coordinator interface {
	// Window fetches the persisted window without reporting anything. Called at startup:
	// this single call is what makes the budget survive a restart.
	Window(ctx context.Context) (Window, error)
	// Report submits bytes measured since the last accepted report and returns the
	// resulting window. reportID must be echoed unchanged on retry — the coordinator
	// deduplicates on it, so a lost response costs nothing and a retry cannot
	// double-count.
	Report(ctx context.Context, reportID string, bytes uint64) (Window, error)
}

// Budget is the local meter and policy. It implements throttle.Meter, so any shaped
// transfer constructed with throttle.NewMeteredWriter is also volume-capped; there is no
// way to get shaping without accounting.
type Budget struct {
	clock throttle.Clock
	coord Coordinator
	newID func() string

	mu sync.Mutex
	// window is the last answer received; synced records whether one ever was, because
	// a zero-valued Window and "a coordinator that answered zero" must not look alike.
	window Window
	synced bool
	// localUsed is the enforced number between syncs: the coordinator's used bytes plus
	// everything charged locally since. It only decreases when a coordinator answer says
	// the window rolled, never because of a local guess.
	localUsed uint64
	// unreported are charged bytes not yet handed to the coordinator. pendingID and
	// pendingBytes are a report in flight or a report whose response was lost: the SAME
	// id is retried, which is what makes retrying safe.
	unreported   uint64
	pendingID    string
	pendingBytes uint64
	// lastErr and failures describe the coordinator relationship for the UI. §16 wants
	// the budget "visible in the connector UI", and "we have not reached the coordinator
	// in two hours" is part of what the donor needs to see.
	lastErr  error
	failures int
	// charged and refused are lifetime local counters, for the status surface only.
	charged, refused uint64
}

// New builds a Budget. clock is injected for the same reason throttle injects one:
// asserting a taper or a staleness transition with real sleeps means a slow, flaky test.
func New(coord Coordinator, clock throttle.Clock) (*Budget, error) {
	if coord == nil {
		return nil, errors.New("budget requires a coordinator")
	}
	if clock == nil {
		clock = throttle.SystemClock{}
	}
	return &Budget{coord: coord, clock: clock, newID: randomReportID}, nil
}

// Charge implements throttle.Meter. It never performs I/O: it decides from state already
// in memory, so a slow or dead coordinator cannot stall a transfer, and the reconciliation
// happens on Sync's schedule instead.
func (b *Budget) Charge(n int64) error {
	if n <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.effectiveLimitLocked()
	if b.localUsed >= limit || uint64(n) > limit-b.localUsed {
		b.refused += uint64(n)
		// Refuse the whole chunk rather than granting part of it: a partial grant would
		// mean the caller has to re-chunk mid-write, and Limiter.Burst already sizes
		// chunks small enough that refusing one is cheap.
		return fmt.Errorf("%w: %d bytes requested, %d of %d bytes remaining in the rolling %d-day window",
			ErrExhausted, n, limit-min(b.localUsed, limit), limit, WindowDays)
	}
	b.localUsed += uint64(n)
	b.unreported += uint64(n)
	b.charged += uint64(n)
	return nil
}

// effectiveLimitLocked is the ceiling enforced right now. Before the first successful
// sync that is the conservative cold-start figure, not the coordinator's default: an
// unreachable coordinator must never widen the cap.
func (b *Budget) effectiveLimitLocked() uint64 {
	if !b.synced || b.window.LimitBytes == 0 {
		return ColdStartMonthlyBytes
	}
	return b.window.LimitBytes
}

// Sync reports accumulated bytes and adopts the coordinator's window. Call it at startup
// (it is the restart-survival path) and then on ReportInterval; Run does both.
//
// It reports even zero bytes, because the answer is the point: a donor that moved nothing
// still needs to learn that their window rolled and their allowance came back.
func (b *Budget) Sync(ctx context.Context) error {
	b.mu.Lock()
	// A previous attempt's id is reused verbatim, carrying its original byte count. Newly
	// charged bytes stay in `unreported` for the next report rather than being folded in,
	// because changing the byte count under an id the coordinator may already have
	// accepted would silently drop the difference.
	if b.pendingID == "" {
		b.pendingID, b.pendingBytes = b.newID(), b.unreported
		b.unreported = 0
	}
	id, bytes := b.pendingID, b.pendingBytes
	b.mu.Unlock()

	window, err := b.coord.Report(ctx, id, bytes)

	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.lastErr, b.failures = err, b.failures+1
		// The bytes stay counted locally (localUsed was incremented at Charge time) and
		// the id stays pending, so enforcement continues on the last known limit and
		// tightens to the offline floor once StaleAfter elapses.
		return err
	}
	b.adoptLocked(window)
	b.pendingID, b.pendingBytes = "", 0
	b.lastErr, b.failures = nil, 0
	return nil
}

// Resync adopts the coordinator's window WITHOUT reporting. Used when there is nothing to
// report and no report has ever been sent — reading state must not need a write.
func (b *Budget) Resync(ctx context.Context) error {
	window, err := b.coord.Window(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.lastErr, b.failures = err, b.failures+1
		return err
	}
	b.adoptLocked(window)
	b.lastErr, b.failures = nil, 0
	return nil
}

// adoptLocked installs a coordinator answer as the enforced state.
//
// localUsed becomes the coordinator's exact figure plus whatever was charged locally
// after the reported batch. The coordinator's number is authoritative rather than merged
// with a max(): it is the only value that reflects the rolling window ageing days out,
// and refusing to let it fall would mean a donor's budget never came back.
func (b *Budget) adoptLocked(window Window) {
	window.RetrievedAt = b.clock.Now()
	b.window = window
	b.synced = true
	b.localUsed = window.UsedBytes + b.unreported
}

// Allowance is the enforcement decision handed to the shaper and the UI.
type Allowance struct {
	// BytesPerSecond is the rate cap the BUDGET imposes. Never zero: zero means
	// "unlimited" to throttle.Limiter, so a budget decision can never be encoded as one.
	BytesPerSecond uint64
	// Capped is false when plenty of budget remains and the budget imposes no rate cut,
	// in which case BytesPerSecond is the sustained pace and callers keep their own base
	// rate (see Apply).
	Capped bool
	// Paused is the hard stop: the volume is spent, and Charge is refusing. Expressed
	// separately from a rate because there is no rate that means "no bytes".
	Paused         bool
	RemainingBytes uint64
	LimitBytes     uint64
	// Source and Reason are for the UI and for logs, so a donor asking "why is this
	// slow" gets an answer instead of a mystery.
	Source, Reason string
	// Stale is true when the decision rests on an answer older than StaleAfter, or on no
	// answer at all. It is the honest label for "this is the fail-safe floor".
	Stale bool
}

// Apply combines a base rate with the budget's cap: the SMALLER wins, and base 0
// ("unlimited" in throttle terms) yields the budget's number whenever the budget has an
// opinion. This is the only correct direction — a budget that could raise a rate the
// estimator chose would undo the rate limiter.
func (a Allowance) Apply(base uint64) uint64 {
	if a.Paused {
		// Still a positive rate: the pause is enforced by Charge refusing bytes, and
		// handing 0 to Limiter.SetRate would mean unlimited.
		return throttle.MinBytesPerSecond
	}
	if !a.Capped {
		return base
	}
	if base == 0 || a.BytesPerSecond < base {
		return a.BytesPerSecond
	}
	return base
}

// Allowance computes the current decision.
func (b *Budget) Allowance() Allowance {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.effectiveLimitLocked()
	remaining := uint64(0)
	if limit > b.localUsed {
		remaining = limit - b.localUsed
	}
	floor := sustainedFloor(limit)
	stale := !b.synced || b.clock.Now().Sub(b.window.RetrievedAt) >= StaleAfter

	switch {
	case remaining == 0:
		return Allowance{BytesPerSecond: throttle.MinBytesPerSecond, Capped: true, Paused: true,
			RemainingBytes: 0, LimitBytes: limit, Stale: stale,
			Source: "local-counter", Reason: "monthly budget spent; bulk transfers paused until the rolling window rolls"}
	case !b.synced:
		return Allowance{BytesPerSecond: floor, Capped: true, RemainingBytes: remaining,
			LimitBytes: limit, Stale: true, Source: "cold-start-floor",
			Reason: "no coordinator answer yet; enforcing the sustained floor of a deliberately conservative assumed budget"}
	case stale:
		// The coordinator's own published floor is honoured when it is LOWER than ours.
		if b.window.OfflineFloorBytesPerSecond > 0 && b.window.OfflineFloorBytesPerSecond < floor {
			floor = b.window.OfflineFloorBytesPerSecond
		}
		return Allowance{BytesPerSecond: floor, Capped: true, RemainingBytes: remaining,
			LimitBytes: limit, Stale: true, Source: "offline-floor",
			Reason: "coordinator unreachable or its answer is stale; enforcing the sustained floor, which cannot overrun the budget even if it runs all month"}
	}

	taperFrom := limit / TaperFraction
	if remaining >= taperFrom {
		return Allowance{BytesPerSecond: floor, Capped: false, RemainingBytes: remaining,
			LimitBytes: limit, Source: "coordinator",
			Reason: "budget healthy; the rate limiter's own ceiling applies and the budget imposes no cut"}
	}
	// Cap-aware scheduling: linear taper across the last quarter, bounded below so the
	// donor keeps a usable trickle rather than falling off a cliff.
	rate := floor
	if taperFrom > 0 {
		rate = floor * remaining / taperFrom
	}
	if min := floor / TaperFloorDivisor; rate < min {
		rate = min
	}
	if rate < throttle.MinBytesPerSecond {
		rate = throttle.MinBytesPerSecond
	}
	return Allowance{BytesPerSecond: rate, Capped: true, RemainingBytes: remaining,
		LimitBytes: limit, Source: "coordinator",
		Reason: fmt.Sprintf("approaching the monthly budget (last %d%% remaining); repair and relay work is tapered as §16 requires",
			100/TaperFraction)}
}

// sustainedFloor is limit spread evenly across the window: the rate that, run without
// pause for the whole window, consumes exactly the budget.
//
// It is clamped UP to throttle.MinBytesPerSecond because Limiter refuses anything lower,
// which means for budgets under ~79 GiB the floor is NOT sustainable and the volume is
// held by Charge refusing bytes instead. Saying that here rather than silently returning
// an unenforceable number is the difference between a limit and a claim.
func sustainedFloor(limit uint64) uint64 {
	rate := limit / uint64(WindowSeconds)
	if rate < throttle.MinBytesPerSecond {
		return throttle.MinBytesPerSecond
	}
	if rate > throttle.MaxBytesPerSecond {
		return throttle.MaxBytesPerSecond
	}
	return rate
}

// Run reports on a schedule until ctx ends. It syncs IMMEDIATELY on entry, because that
// first call is the restart-survival path: until it returns, this process is enforcing the
// conservative cold-start floor rather than the donor's real remaining budget.
//
// apply, when non-nil, is called with every new allowance — that is how the decision
// reaches throttle.Limiter.SetRate without this package importing the agent that owns it.
func (b *Budget) Run(ctx context.Context, apply func(Allowance)) {
	notify := func() {
		if apply != nil {
			apply(b.Allowance())
		}
	}
	_ = b.Sync(ctx)
	notify()
	for {
		timer, stop := b.clock.NewTimer(ReportInterval)
		select {
		case <-ctx.Done():
			stop()
			// One last attempt is deliberately NOT made here: shutdown is where a
			// blocking network call hangs an app quit, and the unreported bytes are
			// bounded by one interval by design.
			return
		case <-timer:
			stop()
			_ = b.Sync(ctx)
			notify()
		}
	}
}

// Status is the §16 "visible in the connector UI" surface. Every field is a number this
// package actually holds; nothing is estimated for display.
type Status struct {
	LimitBytes     uint64 `json:"limitBytes"`
	UsedBytes      uint64 `json:"usedBytes"`
	RemainingBytes uint64 `json:"remainingBytes"`
	// UnreportedBytes are charged but not yet handed to the coordinator; PendingBytes are
	// in a report whose outcome is unknown. Both are bytes a crash right now would lose
	// from the persisted window, which is why they are published rather than netted away.
	UnreportedBytes uint64 `json:"unreportedBytes"`
	PendingBytes    uint64 `json:"pendingBytes"`
	WindowDays      int    `json:"windowDays"`
	Paused          bool   `json:"paused"`
	Capped          bool   `json:"capped"`
	BytesPerSecond  uint64 `json:"bytesPerSecond"`
	Source          string `json:"source"`
	Reason          string `json:"reason"`
	Stale           bool   `json:"stale"`
	// Synced distinguishes "no coordinator answer ever" from "answered zero used" — a
	// zero here with Synced false must not be drawn as a measured zero.
	Synced       bool   `json:"synced"`
	LastSyncedAt string `json:"lastSyncedAt,omitempty"`
	FailedSyncs  int    `json:"failedSyncs"`
	LastError    string `json:"lastError,omitempty"`
	ChargedBytes uint64 `json:"chargedBytes"`
	RefusedBytes uint64 `json:"refusedBytes"`
	// Enforced is true because unlike the rate shaper, this budget IS applied to every
	// metered stream. It stays honest about scope in EnforcementNote.
	Enforced        bool   `json:"enforced"`
	EnforcementNote string `json:"enforcementNote"`
	Evidence        string `json:"evidence"`
}

// Snapshot builds the UI/status view.
func (b *Budget) Snapshot() Status {
	allowance := b.Allowance()
	b.mu.Lock()
	defer b.mu.Unlock()
	status := Status{
		LimitBytes: allowance.LimitBytes, UsedBytes: b.localUsed,
		RemainingBytes: allowance.RemainingBytes, UnreportedBytes: b.unreported,
		PendingBytes: b.pendingBytes,
		WindowDays:   WindowDays, Paused: allowance.Paused, Capped: allowance.Capped,
		BytesPerSecond: allowance.BytesPerSecond, Source: allowance.Source,
		Reason: allowance.Reason, Stale: allowance.Stale, Synced: b.synced,
		FailedSyncs: b.failures, ChargedBytes: b.charged, RefusedBytes: b.refused,
		Enforced:        true,
		EnforcementNote: "Enforced locally on every stream built with throttle.NewMeteredWriter/NewMeteredReader. Streams built with the plain constructors are shaped but not volume-capped and must not claim a budget.",
		Evidence:        "locally measured bytes; the coordinator persists the window but cannot verify the measurement",
	}
	if b.synced {
		status.LastSyncedAt = b.window.RetrievedAt.UTC().Format(time.RFC3339)
	}
	if b.lastErr != nil {
		// The message only, never the full error chain: a coordinator URL or token
		// fragment must not reach a status payload.
		status.LastError = "coordinator unreachable"
	}
	return status
}

// randomReportID is 16 random bytes hex-encoded: unique per report per host, matching the
// coordinator's 8..80 character [A-Za-z0-9_-] validation. Not a secret and not derived
// from the byte count — an id that encoded its payload would make a retry with different
// bytes look like the same report.
func randomReportID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		// Time-based fallback. A collision would be treated as a duplicate and silently
		// drop a report's bytes, so the fallback is still high-resolution.
		return fmt.Sprintf("report-%d", time.Now().UnixNano())
	}
	return "r" + hex.EncodeToString(buffer)
}
