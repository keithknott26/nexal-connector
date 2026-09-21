package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/contribution"
	"nexal/connector/internal/throttle"
)

// fixedConditions is the injectable §36.4 probe. The real macOS probe shells out
// to pmset and cannot run in this sandbox, so every wiring test below uses this
// instead — which is the point of the interface, and the same pattern
// throttle.MeteredSource established.
type fixedConditions struct {
	mu sync.Mutex
	s  contribution.Signals
}

func (f *fixedConditions) Signals() contribution.Signals {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.s
}
func (f *fixedConditions) set(s contribution.Signals) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.s = s
}

// clear is an observation where nothing withholds: AC power, ample disk, thermal
// unknown (the honest state on Apple silicon).
func clearSignals() contribution.Signals {
	return contribution.Signals{At: time.Now(), Power: contribution.PowerAC,
		BatteryPercent: -1, DiskKnown: true, FreeDiskBytes: 500 << 30, DiskVolume: "/"}
}

func conditionAgent(t *testing.T) (*Agent, *fixedConditions) {
	t.Helper()
	a, _ := testAgent(t)
	f := &fixedConditions{s: clearSignals()}
	WithConditionSource(f)(a)
	a.RefreshConditions(context.Background())
	return a, f
}

func condition(t *testing.T, a *Agent, name string) contribution.Condition {
	t.Helper()
	for _, c := range a.Snapshot().Contribution.Conditions {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("status reported no %q condition", name)
	return contribution.Condition{}
}

// TestConditionsGateAdmissionForReal is the "not display-only" assertion: each
// §36.4 condition must block the same admission gate that memory headroom and
// owner activity already block.
func TestConditionsGateAdmissionForReal(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*contribution.Signals)
	}{
		{"battery", func(s *contribution.Signals) {
			s.Power, s.BatteryPresent, s.BatteryPercent = contribution.PowerBattery, true, 44
		}},
		{"thermal", func(s *contribution.Signals) { s.Thermal = contribution.ThermalThrottled }},
		{"disk", func(s *contribution.Signals) { s.FreeDiskBytes = 1 << 30 }},
	} {
		a, f := conditionAgent(t)
		if err := a.admitLocked(true); err != nil {
			t.Fatalf("%s: clear conditions already blocked admission: %v", c.name, err)
		}
		s := clearSignals()
		c.mutate(&s)
		f.set(s)
		a.RefreshConditions(context.Background())
		err := a.admitLocked(true)
		if err == nil {
			t.Fatalf("%s: admission allowed while withholding", c.name)
		}
		if !strings.Contains(err.Error(), "withholding contribution") {
			t.Fatalf("%s: blocker does not name the cause: %v", c.name, err)
		}
		// And it must reach a real attempt, not just the predicate.
		if err := a.Execute(context.Background(), attempt()); err == nil {
			t.Fatalf("%s: work executed while withholding", c.name)
		}
		// §26: the blocker is visible in status, in plain language.
		st := a.Snapshot()
		if !st.Contribution.Withholding || st.Contribution.Summary == "" {
			t.Fatalf("%s: status did not surface the withholding state", c.name)
		}
		if !st.Contribution.Enforced || st.Contribution.EnforcedScope == "" {
			t.Fatalf("%s: enforcement was not described", c.name)
		}
	}
}

// TestAutomaticWithholdingNeverTouchesOwnerPause is the design trap, asserted
// directly. An automatic condition must never be written into config.Paused, or
// the owner's deliberate pause becomes indistinguishable from a machine-made one
// and gets silently cleared when the machine cools.
func TestAutomaticWithholdingNeverTouchesOwnerPause(t *testing.T) {
	a, f := conditionAgent(t)
	hot := clearSignals()
	hot.Thermal = contribution.ThermalThrottled
	f.set(hot)
	a.RefreshConditions(context.Background())
	if a.cfg.Paused {
		t.Fatal("a thermal event set the owner's pause flag in memory")
	}
	saved, err := config.Load(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Paused {
		t.Fatal("a thermal event persisted a pause the owner never asked for")
	}
	st := a.Snapshot()
	if st.Paused {
		t.Fatal("status reported an automatic condition as an owner pause")
	}
	if !st.Contribution.Withholding || st.Contribution.OwnerPaused {
		t.Fatalf("the two pauses were merged: %+v", st.Contribution)
	}
	// Cooling down resumes contribution without any persisted state changing.
	f.set(clearSignals())
	a.RefreshConditions(context.Background())
	if a.Snapshot().Contribution.Withholding {
		t.Fatal("a cooled machine kept withholding")
	}
}

// TestEachPauseSurvivesTheOther is the other half: the owner's pause must
// outlive an automatic condition clearing, and an automatic condition must
// outlive the owner resuming.
func TestEachPauseSurvivesTheOther(t *testing.T) {
	a, f := conditionAgent(t)
	if err := a.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	hot := clearSignals()
	hot.Thermal = contribution.ThermalThrottled
	f.set(hot)
	a.RefreshConditions(context.Background())
	f.set(clearSignals())
	a.RefreshConditions(context.Background())
	// The machine cooled. The owner's pause must still be in force, in memory and
	// on disk.
	if !a.cfg.Paused || !a.Snapshot().Paused {
		t.Fatal("cooling down cleared the owner's deliberate pause")
	}
	saved, err := config.Load(a.path)
	if err != nil || !saved.Paused {
		t.Fatalf("owner pause lost from disk: %v %+v", err, saved)
	}
	// Now the owner resumes while the machine is hot: automatic withholding must
	// still hold, and admission must still refuse.
	f.set(hot)
	a.RefreshConditions(context.Background())
	if err := a.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	a.Refresh(context.Background())
	if !a.Snapshot().Contribution.Withholding {
		t.Fatal("an owner resume cleared an automatic thermal condition")
	}
	if a.admitLocked(true) == nil {
		t.Fatal("resume overrode a live thermal condition")
	}
}

// TestUnknownConditionsKeepTheHostContributing is the fleet-safety property at
// the wiring layer: an agent with no probe at all, and an agent whose probe
// returns nothing, must both keep working with the reason visible.
func TestUnknownConditionsKeepTheHostContributing(t *testing.T) {
	bare, _ := testAgent(t)
	if err := bare.admitLocked(true); err != nil {
		t.Fatalf("an agent with no condition probe was blocked: %v", err)
	}
	st := bare.Snapshot()
	if st.Contribution.Withholding {
		t.Fatal("a missing probe withheld contribution")
	}
	if !strings.Contains(st.Contribution.EnforcedScope, "no platform condition probe") {
		t.Fatalf("a missing probe was not disclosed: %q", st.Contribution.EnforcedScope)
	}
	a, f := conditionAgent(t)
	f.set(contribution.Signals{At: time.Now(), BatteryPercent: -1})
	a.RefreshConditions(context.Background())
	if a.admitLocked(true) != nil || a.Snapshot().Contribution.Withholding {
		t.Fatal("an all-unknown probe stopped the host")
	}
	for _, name := range []string{"power", "thermal", "free disk"} {
		if c := condition(t, a, name); c.Known || c.Reason == "" {
			t.Errorf("%s: unknown state was not explained: %+v", name, c)
		}
	}
}

// TestOwnerActivityIsForwardedNotReimplemented asserts the integration with the
// existing Telemetry dimension rather than a second idle detector.
func TestOwnerActivityIsForwardedNotReimplemented(t *testing.T) {
	a, _ := conditionAgent(t)
	a.probe = func(context.Context) Telemetry {
		return Telemetry{Known: true, Synthetic: true, OwnerActive: true, IdleSeconds: 2,
			AvailableMemoryBytes: 1 << 30, TotalMemoryBytes: 2 << 30}
	}
	a.Refresh(context.Background())
	c := condition(t, a, "owner activity")
	if !c.Known || !c.Withholding || !strings.Contains(c.Value, "idle 2s") {
		t.Fatalf("owner activity not forwarded from Telemetry: %+v", c)
	}
	// Stale telemetry reports unknown here even though admission refuses it for
	// its own, stricter reason.
	a.mu.Lock()
	a.telemetryAt = time.Now().Add(-time.Minute)
	a.mu.Unlock()
	if condition(t, a, "owner activity").Known {
		t.Fatal("stale telemetry was reported as a known owner-activity verdict")
	}
}

// heartbeatRecorder captures what the coordinator is told, which is where
// withholding stops being local: a withholding host must stop being OFFERED work.
type heartbeatRecorder struct {
	fakeAPI
	mu   sync.Mutex
	last client.Heartbeat
}

func (h *heartbeatRecorder) Heartbeat(_ context.Context, _ string, b client.Heartbeat) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = b
	return nil
}
func (h *heartbeatRecorder) latest() client.Heartbeat {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

func TestWithholdingStopsCoordinatorDispatch(t *testing.T) {
	a, f := conditionAgent(t)
	rec := &heartbeatRecorder{}
	a.api = rec
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.latest().OwnerActive {
		t.Fatal("a clear host advertised itself as unavailable")
	}
	battery := clearSignals()
	battery.Power, battery.BatteryPresent = contribution.PowerBattery, true
	f.set(battery)
	a.RefreshConditions(context.Background())
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	// This is the flag the coordinator uses to stop sending work. It is the only
	// field the wire protocol has, which is why the reason stays local (§26).
	if !rec.latest().OwnerActive {
		t.Fatal("a host on battery kept advertising itself as available")
	}
}

func TestStaleConditionsSurviveAPolicyChange(t *testing.T) {
	a, f := conditionAgent(t)
	battery := clearSignals()
	battery.Power, battery.BatteryPresent = contribution.PowerBattery, true
	f.set(battery)
	a.RefreshConditions(context.Background())
	policy := a.cfg.ResourcePolicy()
	policy.IdleSeconds = 600
	if err := a.SetResourcePolicy(policy); err != nil {
		t.Fatal(err)
	}
	// A consent change fences observations that could wrongly PERMIT work. It must
	// not discard the ones that correctly withhold it: editing a policy is not a
	// way to plug in a laptop.
	if !a.Snapshot().Contribution.Withholding {
		t.Fatal("a policy change cleared a live battery condition")
	}
}

func TestOwnerTunableDiskFloorReachesTheDecision(t *testing.T) {
	a, f := conditionAgent(t)
	low := clearSignals()
	low.FreeDiskBytes = 3 << 30
	f.set(low)
	a.RefreshConditions(context.Background())
	if !a.Snapshot().Contribution.Withholding {
		t.Fatal("3 GiB free did not trip the default 10 GiB floor")
	}
	policy := a.cfg.ResourcePolicy()
	policy.MinFreeDiskBytes = 2 << 30
	if err := a.SetResourcePolicy(policy); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Contribution.Withholding {
		t.Fatal("the owner's lowered disk floor was ignored by the live decision")
	}
	if got := a.Snapshot().ResourcePolicy.EffectiveMinFreeDisk(); got != 2<<30 {
		t.Fatalf("policy accessor disagreed with the decision: %d", got)
	}
}

// bridgeDouble stands in for the single Swift→Go bridge, which does not exist
// yet. The test exercises the seam so contribution.PlatformBridge is a contract
// rather than a comment, and it covers the one thing only the bridge can do:
// report nominal, which the pmset parser is forbidden to infer.
type bridgeDouble struct {
	state contribution.ThermalState
	limit int
}

func (b *bridgeDouble) Thermal() (contribution.ThermalState, int) { return b.state, b.limit }

// One bridge, two signals: satisfying throttle.MeteredSource is what makes the
// consolidation real rather than aspirational.
func (*bridgeDouble) Metered() throttle.MeteredState { return throttle.MeteredNo }

func TestPlatformBridgeOverridesThePmsetReading(t *testing.T) {
	a, f := conditionAgent(t)
	hot := clearSignals()
	hot.Thermal = contribution.ThermalThrottled
	f.set(hot)
	a.RefreshConditions(context.Background())
	if !a.Snapshot().Contribution.Withholding {
		t.Fatal("pmset throttle reading ignored")
	}
	WithPlatformBridge(&bridgeDouble{state: contribution.ThermalNominal})(a)
	st := a.Snapshot()
	if st.Contribution.Withholding {
		t.Fatal("an authoritative nominal verdict did not override pmset")
	}
	if st.Contribution.ThermalSource != "ProcessInfo.thermalState bridge" {
		t.Fatalf("thermal provenance not reported: %q", st.Contribution.ThermalSource)
	}
}
