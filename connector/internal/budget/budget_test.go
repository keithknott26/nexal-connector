package budget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/throttle"
)

// Every test here is OFFLINE by construction: the coordinator is an interface and the
// clock is fake, so nothing in this file can reach a network or spend wall-clock seconds
// proving a taper.

const gib = uint64(1024 * 1024 * 1024)

// fakeClock is local rather than borrowed from internal/throttle, whose fake lives in a
// _test.go file and is therefore unimportable. Timers fire only when the test advances
// the clock, so a schedule can be asserted deterministically.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}
type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	return timer.ch, func() {}
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	pending := c.timers
	c.timers = nil
	var remaining []*fakeTimer
	for _, timer := range pending {
		if !timer.at.After(c.now) {
			timer.ch <- c.now
		} else {
			remaining = append(remaining, timer)
		}
	}
	c.timers = append(c.timers, remaining...)
	c.mu.Unlock()
}

// fakeCoordinator records exactly what was sent, which is how idempotency is asserted:
// the test checks the report ID did not change across a retry.
type fakeCoordinator struct {
	mu      sync.Mutex
	window  Window
	err     error
	reports []struct {
		id    string
		bytes uint64
	}
	windowCalls int
}

func (f *fakeCoordinator) Window(context.Context) (Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windowCalls++
	if f.err != nil {
		return Window{}, f.err
	}
	return f.window, nil
}
func (f *fakeCoordinator) Report(_ context.Context, id string, reported uint64) (Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, struct {
		id    string
		bytes uint64
	}{id, reported})
	if f.err != nil {
		return Window{}, f.err
	}
	// Mirrors the coordinator: the returned window already includes the accepted bytes.
	f.window.UsedBytes += reported
	if f.window.LimitBytes > f.window.UsedBytes {
		f.window.RemainingBytes = f.window.LimitBytes - f.window.UsedBytes
	} else {
		f.window.RemainingBytes, f.window.Exhausted = 0, true
	}
	return f.window, nil
}
func (f *fakeCoordinator) sent() []struct {
	id    string
	bytes uint64
} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]struct {
		id    string
		bytes uint64
	}(nil), f.reports...)
}

func window(limit, used uint64) Window {
	remaining := uint64(0)
	if limit > used {
		remaining = limit - used
	}
	sustained := limit / uint64(WindowSeconds)
	return Window{LimitBytes: limit, UsedBytes: used, RemainingBytes: remaining,
		SustainedBytesPerSecond: sustained, OfflineFloorBytesPerSecond: sustained,
		WindowDays: WindowDays, Exhausted: remaining == 0}
}

func newBudget(t *testing.T, w Window) (*Budget, *fakeCoordinator, *fakeClock) {
	t.Helper()
	coordinator, clock := &fakeCoordinator{window: w}, newClock()
	budget, err := New(coordinator, clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return budget, coordinator, clock
}

func TestNewRejectsMissingCoordinator(t *testing.T) {
	if _, err := New(nil, newClock()); err == nil {
		t.Fatal("a budget with no coordinator would have no window to enforce and must be refused")
	}
}

// THE POINT OF THE WHOLE FEATURE: a process that just started, holding no memory of any
// previous one, enforces the bytes an earlier process already moved.
func TestBudgetSurvivesRestart(t *testing.T) {
	budget, _, _ := newBudget(t, window(250*gib, 200*gib))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	allowance := budget.Allowance()
	if allowance.RemainingBytes != 50*gib {
		t.Fatalf("a restarted connector must inherit the persisted usage: remaining %d, want %d",
			allowance.RemainingBytes, 50*gib)
	}
	// The process-lifetime counter this replaces would have allowed the whole limit again.
	if err := budget.Charge(int64(60 * gib)); !errors.Is(err, throttle.ErrBudgetExhausted) {
		t.Fatalf("charging past the inherited usage must be refused, got %v", err)
	}
	if err := budget.Charge(int64(10 * gib)); err != nil {
		t.Fatalf("charging inside the inherited remainder must succeed, got %v", err)
	}
}

func TestColdStartEnforcesConservativeFloorNotUnlimited(t *testing.T) {
	budget, _, _ := newBudget(t, window(250*gib, 0))
	allowance := budget.Allowance() // no Sync: nothing has ever been heard
	if !allowance.Capped || allowance.Source != "cold-start-floor" {
		t.Fatalf("an unsynced budget must cap: %+v", allowance)
	}
	if allowance.BytesPerSecond == 0 {
		t.Fatal("a zero rate means UNLIMITED to throttle.Limiter, which is the opposite of failing safe")
	}
	if allowance.LimitBytes != ColdStartMonthlyBytes {
		t.Fatalf("cold start must assume the conservative budget, got %d", allowance.LimitBytes)
	}
	if ColdStartMonthlyBytes >= 250*gib {
		t.Fatal("cold start must be SMALLER than the coordinator default, or an outage grants extra allowance")
	}
	if allowance.Apply(0) != allowance.BytesPerSecond {
		t.Fatal("an unlimited base rate must be replaced by the budget floor, never left unlimited")
	}
	if !allowance.Stale {
		t.Fatal("a decision resting on no coordinator answer must be labelled stale")
	}
}

func TestOfflineFallsBackToSustainedFloorAndKeepsEnforcing(t *testing.T) {
	budget, coordinator, clock := newBudget(t, window(250*gib, 0))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if budget.Allowance().Capped {
		t.Fatal("a healthy budget should impose no rate cut")
	}
	coordinator.mu.Lock()
	coordinator.err = errors.New("dial tcp: no route to host")
	coordinator.mu.Unlock()
	if err := budget.Sync(context.Background()); err == nil {
		t.Fatal("expected the sync to fail")
	}
	// One failure is a blip and must not collapse the throttle.
	if budget.Allowance().Source == "offline-floor" {
		t.Fatal("a single failed sync must not immediately drop to the floor")
	}
	clock.advance(StaleAfter)
	allowance := budget.Allowance()
	if allowance.Source != "offline-floor" || !allowance.Stale || !allowance.Capped {
		t.Fatalf("a stale answer must fail safe to the floor: %+v", allowance)
	}
	want := 250 * gib / uint64(WindowSeconds)
	if allowance.BytesPerSecond != want {
		t.Fatalf("offline floor %d, want the sustained rate %d", allowance.BytesPerSecond, want)
	}
	// The arithmetic that makes the floor safe: running it for the whole window cannot
	// exceed the budget.
	if allowance.BytesPerSecond*uint64(WindowSeconds) > 250*gib {
		t.Fatal("the offline floor must not be able to overrun the budget even if it runs all month")
	}
	if allowance.Apply(1<<30) != allowance.BytesPerSecond {
		t.Fatal("the floor must win over a larger base rate")
	}
}

func TestExhaustedPausesAndNeverReturnsRateZero(t *testing.T) {
	budget, _, _ := newBudget(t, window(2*gib, 2*gib))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	allowance := budget.Allowance()
	if !allowance.Paused || allowance.RemainingBytes != 0 {
		t.Fatalf("a spent budget must pause: %+v", allowance)
	}
	if allowance.BytesPerSecond == 0 || allowance.Apply(0) == 0 {
		t.Fatal("pause is enforced by refusing bytes; a zero rate would read as unlimited")
	}
	if err := budget.Charge(1); !errors.Is(err, ErrExhausted) || !errors.Is(err, throttle.ErrBudgetExhausted) {
		t.Fatalf("a spent budget must refuse a single byte with both sentinels, got %v", err)
	}
}

func TestTaperBeforeTheWallAsSection16Requires(t *testing.T) {
	limit := 250 * gib
	budget, _, _ := newBudget(t, window(limit, limit-limit/TaperFraction/2)) // half of the taper band left
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	allowance := budget.Allowance()
	floor := limit / uint64(WindowSeconds)
	if !allowance.Capped || allowance.Paused {
		t.Fatalf("approaching the limit must taper, not stop: %+v", allowance)
	}
	if allowance.BytesPerSecond >= floor {
		t.Fatalf("taper rate %d must be below the sustained pace %d", allowance.BytesPerSecond, floor)
	}
	if allowance.BytesPerSecond < floor/TaperFloorDivisor {
		t.Fatalf("taper rate %d fell below the documented floor %d", allowance.BytesPerSecond, floor/TaperFloorDivisor)
	}
	if allowance.BytesPerSecond < throttle.MinBytesPerSecond {
		t.Fatal("a rate below throttle.MinBytesPerSecond cannot be set on the limiter")
	}
	// Monotone: less remaining must never mean a faster rate.
	previous := allowance.BytesPerSecond
	if err := budget.Charge(int64(limit / TaperFraction / 4)); err != nil {
		t.Fatalf("Charge: %v", err)
	}
	if next := budget.Allowance().BytesPerSecond; next > previous {
		t.Fatalf("rate rose from %d to %d as the budget shrank", previous, next)
	}
}

func TestBudgetReturnsWhenTheWindowRolls(t *testing.T) {
	budget, coordinator, _ := newBudget(t, window(2*gib, 2*gib))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !budget.Allowance().Paused {
		t.Fatal("expected a paused budget")
	}
	// The coordinator's counter falls as day buckets age out of the rolling window. The
	// local state must follow it down, or a donor's budget would never come back.
	coordinator.mu.Lock()
	coordinator.window = window(2*gib, 0)
	coordinator.mu.Unlock()
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	allowance := budget.Allowance()
	if allowance.Paused || allowance.RemainingBytes != 2*gib {
		t.Fatalf("a rolled window must restore the allowance: %+v", allowance)
	}
	if err := budget.Charge(int64(gib)); err != nil {
		t.Fatalf("Charge after roll: %v", err)
	}
}

func TestReportIsRetriedWithTheSameIdentifier(t *testing.T) {
	budget, coordinator, _ := newBudget(t, window(250*gib, 0))
	if err := budget.Charge(int64(gib)); err != nil {
		t.Fatalf("Charge: %v", err)
	}
	coordinator.mu.Lock()
	coordinator.err = errors.New("502 bad gateway")
	coordinator.mu.Unlock()
	if err := budget.Sync(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	// More bytes move while the coordinator is down.
	if err := budget.Charge(int64(gib)); err != nil {
		t.Fatalf("Charge: %v", err)
	}
	if err := budget.Sync(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	sent := coordinator.sent()
	if len(sent) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(sent))
	}
	if sent[0].id != sent[1].id {
		t.Fatalf("a retry must reuse the report id (%q then %q) or the coordinator cannot deduplicate",
			sent[0].id, sent[1].id)
	}
	if sent[0].bytes != sent[1].bytes {
		t.Fatalf("a retried id must carry the same byte count, got %d then %d", sent[0].bytes, sent[1].bytes)
	}
	coordinator.mu.Lock()
	coordinator.err = nil
	coordinator.mu.Unlock()
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	sent = coordinator.sent()
	if sent[2].id != sent[1].id {
		t.Fatal("the pending id must be retried until it is accepted")
	}
	// The bytes charged during the outage go out under a NEW id, unchanged in total.
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	sent = coordinator.sent()
	if sent[3].id == sent[2].id {
		t.Fatal("a fresh batch must not reuse an accepted id, or its bytes are deduplicated away")
	}
	if total := sent[2].bytes + sent[3].bytes; total != 2*gib {
		t.Fatalf("every charged byte must reach the coordinator exactly once, got %d", total)
	}
}

func TestLocalCountingContinuesWhileTheCoordinatorIsDown(t *testing.T) {
	budget, coordinator, clock := newBudget(t, window(2*gib, 0))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	coordinator.mu.Lock()
	coordinator.err = errors.New("offline")
	coordinator.mu.Unlock()
	if err := budget.Sync(context.Background()); err == nil {
		t.Fatal("expected the sync to fail")
	}
	clock.advance(StaleAfter)
	// The donor keeps moving bytes with no coordinator at all. The cap must still bind.
	if err := budget.Charge(int64(2 * gib)); err != nil {
		t.Fatalf("Charge: %v", err)
	}
	if err := budget.Charge(1); !errors.Is(err, throttle.ErrBudgetExhausted) {
		t.Fatalf("the cap must bind offline, got %v", err)
	}
	status := budget.Snapshot()
	if status.UnreportedBytes != 2*gib || !status.Stale || status.FailedSyncs == 0 {
		t.Fatalf("status must show the unreported backlog and the outage: %+v", status)
	}
	if status.LastError == "" {
		t.Fatal("an outage must be visible in the UI surface")
	}
}

func TestSnapshotDistinguishesUnsyncedFromZero(t *testing.T) {
	budget, _, _ := newBudget(t, window(250*gib, 0))
	status := budget.Snapshot()
	if status.Synced {
		t.Fatal("nothing has been heard yet; Synced must be false so a zero is not drawn as measured")
	}
	if status.LastSyncedAt != "" {
		t.Fatal("no sync time may be published before a sync")
	}
	if !status.Enforced || status.EnforcementNote == "" || status.Evidence == "" {
		t.Fatal("the status surface must state what is enforced and on what evidence")
	}
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if status = budget.Snapshot(); !status.Synced || status.LastSyncedAt == "" {
		t.Fatalf("after a sync the time must be published: %+v", status)
	}
}

func TestApplyNeverRaisesTheBaseRate(t *testing.T) {
	cases := []struct {
		name      string
		allowance Allowance
		base      uint64
		want      uint64
	}{
		{"healthy budget leaves the base alone", Allowance{Capped: false, BytesPerSecond: 99}, 1000, 1000},
		{"cap below the base wins", Allowance{Capped: true, BytesPerSecond: 500}, 1000, 500},
		{"cap above the base loses", Allowance{Capped: true, BytesPerSecond: 5000}, 1000, 1000},
		{"unlimited base adopts the cap", Allowance{Capped: true, BytesPerSecond: 500}, 0, 500},
		{"unlimited base with a healthy budget stays unlimited", Allowance{Capped: false, BytesPerSecond: 500}, 0, 0},
		{"paused still yields a positive rate", Allowance{Paused: true, Capped: true, BytesPerSecond: 1}, 0, throttle.MinBytesPerSecond},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.allowance.Apply(testCase.base); got != testCase.want {
				t.Fatalf("Apply(%d) = %d, want %d", testCase.base, got, testCase.want)
			}
		})
	}
}

func TestSustainedFloorIsClampedToWhatTheLimiterCanHold(t *testing.T) {
	// A 1 GiB budget implies ~414 B/s, far below MinBytesPerSecond. The floor is clamped
	// up, which means the VOLUME of a small budget is held by pausing, not by shaping —
	// the honest consequence, asserted rather than hidden.
	if got := sustainedFloor(gib); got != throttle.MinBytesPerSecond {
		t.Fatalf("small budgets must clamp to the limiter minimum, got %d", got)
	}
	if got := sustainedFloor(4 * 1024 * 1024 * gib); got != throttle.MaxBytesPerSecond {
		t.Fatalf("absurd budgets must clamp to the limiter maximum, got %d", got)
	}
	if got := sustainedFloor(250 * gib); got != 250*gib/uint64(WindowSeconds) {
		t.Fatalf("a normal budget must spread evenly, got %d", got)
	}
}

func TestRunSyncsImmediatelyThenOnSchedule(t *testing.T) {
	budget, coordinator, clock := newBudget(t, window(250*gib, 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	applied := make(chan Allowance, 8)
	go budget.Run(ctx, func(a Allowance) { applied <- a })
	// The immediate sync is the restart-survival path: enforcement must not wait an
	// interval to learn the real remaining budget.
	select {
	case allowance := <-applied:
		if allowance.Source != "coordinator" {
			t.Fatalf("the first allowance should come from the immediate sync: %+v", allowance)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not sync on entry")
	}
	clock.advance(ReportInterval)
	select {
	case <-applied:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not sync on the interval")
	}
	if got := len(coordinator.sent()); got < 2 {
		t.Fatalf("want at least 2 reports, got %d", got)
	}
	cancel()
}

func TestChargeIsSafeUnderConcurrency(t *testing.T) {
	// -race guard for the real shape of usage: many streams charging while the reporter
	// syncs. The invariant is the one that matters, not a timing: total charged bytes can
	// never exceed the limit.
	limit := 64 * uint64(1024*1024)
	budget, _, _ := newBudget(t, window(limit, 0))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var granted int64
	var mu sync.Mutex
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < 500; i++ {
				chunk := int64(64 * 1024)
				if err := budget.Charge(chunk); err == nil {
					mu.Lock()
					granted += chunk
					mu.Unlock()
				}
			}
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		for i := 0; i < 20; i++ {
			_ = budget.Sync(context.Background())
		}
	}()
	group.Wait()
	if uint64(granted) > limit {
		t.Fatalf("granted %d bytes against a %d byte budget", granted, limit)
	}
	if uint64(granted) == 0 {
		t.Fatal("nothing was granted, so the test proved nothing")
	}
}

func TestReportIdentifierMatchesCoordinatorValidation(t *testing.T) {
	// The coordinator validates 8..80 characters of [A-Za-z0-9_-]. A drift here shows up
	// as every report failing with a 400, which would look like an outage and silently
	// park the fleet on the offline floor.
	for i := 0; i < 64; i++ {
		id := randomReportID()
		if len(id) < 8 || len(id) > 80 {
			t.Fatalf("report id %q has length %d, outside the coordinator's 8..80", id, len(id))
		}
		for _, r := range id {
			if !(r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				t.Fatalf("report id %q contains %q, which the coordinator rejects", id, r)
			}
		}
	}
}

// The seam actually holding: a shaped writer refuses bytes once the volume is spent.
func TestMeteredWriterStopsAtTheMonthlyVolume(t *testing.T) {
	limit := uint64(512 * 1024)
	budget, _, _ := newBudget(t, window(limit, 0))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// A rate high enough that the shaper never delays: this test is about volume.
	limiter, err := throttle.NewLimiter(throttle.MaxBytesPerSecond, 64*1024, throttle.SystemClock{})
	if err != nil {
		t.Fatalf("NewLimiter: %v", err)
	}
	sink := &bytes.Buffer{}
	writer, err := throttle.NewMeteredWriter(context.Background(), sink, limiter, budget)
	if err != nil {
		t.Fatalf("NewMeteredWriter: %v", err)
	}
	payload := make([]byte, 64*1024)
	var written int
	var lastErr error
	for i := 0; i < 32; i++ {
		n, err := writer.Write(payload)
		written += n
		if err != nil {
			lastErr = err
			break
		}
	}
	if !errors.Is(lastErr, throttle.ErrBudgetExhausted) {
		t.Fatalf("a metered writer must stop with ErrBudgetExhausted, got %v", lastErr)
	}
	if uint64(written) > limit {
		t.Fatalf("wrote %d bytes against a %d byte monthly budget", written, limit)
	}
	if uint64(sink.Len()) > limit {
		t.Fatalf("%d bytes reached the wire against a %d byte budget", sink.Len(), limit)
	}
	if written == 0 {
		t.Fatal("the writer moved nothing, so the budget was not the thing under test")
	}
}

func TestMeteredConstructorsRefuseANilMeter(t *testing.T) {
	limiter, err := throttle.NewLimiter(throttle.ConservativeBytesPerSecond, 64*1024, throttle.SystemClock{})
	if err != nil {
		t.Fatalf("NewLimiter: %v", err)
	}
	if _, err := throttle.NewMeteredWriter(context.Background(), &bytes.Buffer{}, limiter, nil); err == nil {
		t.Fatal("a metered writer with no meter would silently be unbudgeted")
	}
	if _, err := throttle.NewMeteredReader(context.Background(), bytes.NewReader(nil), limiter, nil); err == nil {
		t.Fatal("a metered reader with no meter would silently be unbudgeted")
	}
}

func TestMeteredReaderChargesInboundBytes(t *testing.T) {
	limit := uint64(128 * 1024)
	budget, _, _ := newBudget(t, window(limit, 0))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	limiter, err := throttle.NewLimiter(throttle.MaxBytesPerSecond, 32*1024, throttle.SystemClock{})
	if err != nil {
		t.Fatalf("NewLimiter: %v", err)
	}
	source := bytes.NewReader(make([]byte, 1024*1024))
	reader, err := throttle.NewMeteredReader(context.Background(), source, limiter, budget)
	if err != nil {
		t.Fatalf("NewMeteredReader: %v", err)
	}
	buffer := make([]byte, 32*1024)
	var read int
	var lastErr error
	for i := 0; i < 64; i++ {
		n, err := reader.Read(buffer)
		read += n
		if err != nil {
			lastErr = err
			break
		}
	}
	if !errors.Is(lastErr, throttle.ErrBudgetExhausted) {
		t.Fatalf("inbound bytes count against a residential cap too, got %v", lastErr)
	}
	if uint64(read) > limit {
		t.Fatalf("read %d bytes against a %d byte budget", read, limit)
	}
}

func TestUnmeteredWriterIsUnchanged(t *testing.T) {
	// Regression guard for the seam: the pre-budget constructors must keep working, and
	// must NOT be silently volume-capped by a meter they never received.
	limiter, err := throttle.NewLimiter(throttle.MaxBytesPerSecond, 8*1024, throttle.SystemClock{})
	if err != nil {
		t.Fatalf("NewLimiter: %v", err)
	}
	sink := &bytes.Buffer{}
	writer, err := throttle.NewWriter(context.Background(), sink, limiter)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	payload := make([]byte, 40*1024)
	if n, err := writer.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("plain writer wrote %d bytes, err %v", n, err)
	}
	if sink.Len() != len(payload) {
		t.Fatalf("plain writer delivered %d of %d bytes", sink.Len(), len(payload))
	}
}

func TestChargeIgnoresNonPositiveByteCounts(t *testing.T) {
	budget, _, _ := newBudget(t, window(gib, 0))
	for _, n := range []int64{0, -1, -1 << 40} {
		if err := budget.Charge(n); err != nil {
			t.Fatalf("Charge(%d) = %v, want nil", n, err)
		}
	}
	if used := budget.Snapshot().UsedBytes; used != 0 {
		t.Fatalf("a non-positive charge must not move the counter, used %d", used)
	}
}

func TestResyncReadsWithoutReporting(t *testing.T) {
	budget, coordinator, _ := newBudget(t, window(250*gib, 10*gib))
	if err := budget.Resync(context.Background()); err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if len(coordinator.sent()) != 0 {
		t.Fatal("Resync must not write a report; reading the window is not an accounting event")
	}
	if got := budget.Allowance().RemainingBytes; got != 240*gib {
		t.Fatalf("remaining %d, want %d", got, 240*gib)
	}
	coordinator.mu.Lock()
	coordinator.err = errors.New("offline")
	coordinator.mu.Unlock()
	if err := budget.Resync(context.Background()); err == nil {
		t.Fatal("expected the failure to surface")
	}
	if got := budget.Allowance().RemainingBytes; got != 240*gib {
		t.Fatalf("a failed resync must leave the last known window in force, got %d", got)
	}
}

func TestFormattedRefusalNamesTheWindow(t *testing.T) {
	budget, _, _ := newBudget(t, window(gib, gib))
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	err := budget.Charge(1024)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	// The message is what a donor sees in a log; it must say WHY, not just "denied".
	if want := fmt.Sprintf("%d-day window", WindowDays); !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Fatalf("refusal %q should explain the window", err)
	}
}
