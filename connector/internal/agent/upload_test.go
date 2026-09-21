package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/throttle"
)

type stubMetered struct{ state throttle.MeteredState }

func (s stubMetered) Metered() throttle.MeteredState { return s.state }

// §26 and §36.4 require that an owner can always see WHY their Mac is behaving as
// it is. For the bandwidth dimension that means the number, its origin, and —
// because no bulk upload path exists yet — the fact that nothing obeys it.
func TestStatusExplainsTheUploadCeilingAndAdmitsItIsNotEnforced(t *testing.T) {
	a, _ := testAgent(t)
	got := a.Snapshot().UploadThrottle
	if got.Mode != config.UploadModeAuto {
		t.Fatalf("default mode %q, want auto", got.Mode)
	}
	if got.EffectiveBytesPerSecond != throttle.ConservativeBytesPerSecond {
		t.Fatalf("default ceiling %d, want the conservative default", got.EffectiveBytesPerSecond)
	}
	if got.Enforced {
		t.Fatal("status claims the throttle is enforced; there is no bulk upload path to enforce it on")
	}
	for _, want := range []string{"no bulk upload path", "metered status unknown"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason %q does not mention %q", got.Reason, want)
		}
	}
	if got.MeteredStatus != "unknown" {
		t.Fatalf("metered status %q, want unknown with no OS signal", got.MeteredStatus)
	}
}

// Metered: throttle hard, keep going. The ceiling overrides even explicit
// unlimited mode, because unlimited was chosen for a link nobody was billing by
// the gigabyte, and a rate cap is not a monthly budget — say both out loud.
func TestMeteredPathIsCappedNotPaused(t *testing.T) {
	a, _ := testAgent(t)
	a.metered = stubMetered{throttle.MeteredYes}
	unlimited := a.Snapshot().ResourcePolicy
	unlimited.UploadMode = config.UploadModeUnlimited
	if err := a.SetResourcePolicy(unlimited); err != nil {
		t.Fatal(err)
	}
	got := a.Snapshot().UploadThrottle
	if got.EffectiveBytesPerSecond != throttle.MeteredCeilingBytesPerSecond {
		t.Fatalf("metered ceiling %d, want %d", got.EffectiveBytesPerSecond, throttle.MeteredCeilingBytesPerSecond)
	}
	if got.EffectiveBytesPerSecond == 0 {
		t.Fatal("a metered path was left unthrottled or paused")
	}
	if !strings.Contains(got.Reason, "NOT by a monthly volume budget") {
		t.Fatalf("reason hides the missing §16 monthly budget: %q", got.Reason)
	}
	if got.MeteredStatus != "metered" {
		t.Fatalf("metered status %q, want metered", got.MeteredStatus)
	}
}

// A manual ceiling is the owner's decision and outranks anything measured.
func TestManualCeilingOutranksMeasurement(t *testing.T) {
	a, _ := testAgent(t)
	if err := a.ApplyMeasuredUploadLimit(8 << 20); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().UploadThrottle; got.EffectiveBytesPerSecond != 8<<20 ||
		!strings.Contains(got.Source, "measured") {
		t.Fatalf("measurement did not take effect in auto mode: %+v", got)
	}
	manual := a.Snapshot().ResourcePolicy
	manual.UploadMode, manual.UploadLimitBytesPerSecond = config.UploadModeManual, 1<<20
	if err := a.SetResourcePolicy(manual); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().UploadThrottle; got.EffectiveBytesPerSecond != 1<<20 {
		t.Fatalf("manual ceiling %d, want 1 MiB/s", got.EffectiveBytesPerSecond)
	}
	// And a measurement arriving afterwards must not quietly rewrite a policy the
	// owner set by hand.
	if err := a.ApplyMeasuredUploadLimit(9 << 20); err == nil {
		t.Fatal("a measurement was stored under a manual policy")
	}
	if got := a.Snapshot().UploadThrottle; got.EffectiveBytesPerSecond != 1<<20 {
		t.Fatal("a refused measurement still changed the ceiling")
	}
}

// A consent change carries mode and manual limit; it must not erase what auto has
// learned about this link, or every policy edit would restart the cold start.
func TestPolicyUpdatePreservesTheMeasuredRate(t *testing.T) {
	a, _ := testAgent(t)
	if err := a.ApplyMeasuredUploadLimit(3 << 20); err != nil {
		t.Fatal(err)
	}
	next := a.Snapshot().ResourcePolicy
	next.IdleSeconds = 600
	next.MeasuredUploadBytesPerSecond = 99 << 20 // a caller must not assert this
	if err := a.SetResourcePolicy(next); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().ResourcePolicy; got.MeasuredUploadBytesPerSecond != 3<<20 {
		t.Fatalf("measured rate became %d across a policy update, want 3 MiB/s", got.MeasuredUploadBytesPerSecond)
	}
	persisted, err := config.Load(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MeasuredUploadBytesPerSecond != 3<<20 {
		t.Fatalf("persisted measured rate %d, want 3 MiB/s", persisted.MeasuredUploadBytesPerSecond)
	}
}

func TestMeasuredRateOutsideBoundsIsRefused(t *testing.T) {
	a, _ := testAgent(t)
	for _, bps := range []uint64{0, throttle.MinBytesPerSecond - 1, throttle.MaxBytesPerSecond + 1} {
		if err := a.ApplyMeasuredUploadLimit(bps); err == nil {
			t.Errorf("accepted an out-of-bounds measured rate %d", bps)
		}
	}
	if a.Snapshot().UploadThrottle.EffectiveBytesPerSecond != throttle.ConservativeBytesPerSecond {
		t.Fatal("a rejected measurement moved the ceiling")
	}
}

// The local API is the whole owner-facing surface, so the upload fields must
// survive a real GET/PUT round trip through it, and a legacy three-field PUT must
// still be accepted (the shipped Swift UI cannot be rebuilt in this sandbox).
func TestPolicyEndpointRoundTripsUploadFields(t *testing.T) {
	a, _ := testAgent(t)
	token := strings.Repeat("x", 64)
	handler, err := a.Handler(token)
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) int {
		r := httptest.NewRequest("PUT", "http://127.0.0.1:8788/v1/policy", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:54321"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if err := a.ApplyMeasuredUploadLimit(2 << 20); err != nil {
		t.Fatal(err)
	}
	full, _ := json.Marshal(a.Snapshot().ResourcePolicy)
	if code := put(string(full)); code != 200 {
		t.Fatalf("a policy read from GET was rejected by PUT with %d", code)
	}
	if code := put(`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300}`); code != 200 {
		t.Fatalf("a legacy three-field PUT was rejected with %d", code)
	}
	after := a.Snapshot().ResourcePolicy
	if after.UploadMode != config.UploadModeAuto {
		t.Fatalf("legacy PUT left mode %q, want auto", after.UploadMode)
	}
	if after.MeasuredUploadBytesPerSecond != 2<<20 {
		t.Fatal("a legacy PUT discarded the measured rate")
	}
	if code := put(`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300,"uploadMode":"manual","uploadLimitBytesPerSecond":524288}`); code != 200 {
		t.Fatal("a manual upload policy was rejected over the local API")
	}
	if got := a.Snapshot().UploadThrottle; got.Mode != config.UploadModeManual ||
		got.EffectiveBytesPerSecond != 512<<10 {
		t.Fatalf("manual upload policy did not take effect: %+v", got)
	}
	if code := put(`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300,"uploadMode":"manual"}`); code != 400 {
		t.Fatal("manual mode without a limit was accepted over the local API")
	}
}

// The estimator-to-policy seam, end to end with no network: passive samples in,
// a persisted ceiling out. This is the wiring the future bulk upload path needs;
// nothing calls it in production yet because nothing uploads bulk data yet.
func TestEstimatorDrivesThePersistedCeilingOffline(t *testing.T) {
	a, _ := testAgent(t)
	e, err := throttle.NewEstimator(throttle.SystemClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// One setup measurement, injected, so the test needs no network at all.
	if err := e.RunSetupMeasurement(context.Background(), func(context.Context) (throttle.Sample, error) {
		return throttle.Sample{Bytes: 4 << 20, Elapsed: time.Second}, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, bytes := range []uint64{2 << 20, 3 << 20, 1 << 20, 2500 << 10} {
		if err := e.Observe(throttle.Sample{Bytes: bytes, Elapsed: time.Second, RTT: 20 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	estimate := e.Estimate()
	if err := a.ApplyMeasuredUploadLimit(estimate.BytesPerSecond); err != nil {
		t.Fatal(err)
	}
	status := a.Snapshot().UploadThrottle
	if status.EffectiveBytesPerSecond != estimate.BytesPerSecond {
		t.Fatalf("status ceiling %d, want the estimate %d", status.EffectiveBytesPerSecond, estimate.BytesPerSecond)
	}
	if status.EffectiveBytesPerSecond >= 2<<20 {
		t.Fatalf("ceiling %d leaves no headroom below the observed rates", status.EffectiveBytesPerSecond)
	}
	persisted, err := config.Load(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MeasuredUploadBytesPerSecond != estimate.BytesPerSecond {
		t.Fatal("the measured ceiling did not survive a restart")
	}
}
