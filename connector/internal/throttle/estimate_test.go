package throttle

import (
	"context"
	"errors"
	"testing"
	"time"
)

// sample builds an observation with an exact implied rate, so percentile and
// headroom arithmetic can be asserted rather than eyeballed.
func sample(bytesPerSecond uint64, rtt time.Duration) Sample {
	return Sample{Bytes: bytesPerSecond, Elapsed: time.Second, RTT: rtt}
}

type fixedMetered struct{ state MeteredState }

func (f fixedMetered) Metered() MeteredState { return f.state }

// The cold start is the dangerous moment: no measurement exists, and the wrong
// default is an unshaped uplink during a multi-hour first backup.
func TestUnmeasuredEstimateIsConservativeNeverUnlimited(t *testing.T) {
	e, err := NewEstimator(newFakeClock(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := e.Estimate()
	if got.BytesPerSecond != ConservativeBytesPerSecond {
		t.Fatalf("cold start limit %d, want the conservative default %d", got.BytesPerSecond, ConservativeBytesPerSecond)
	}
	if got.BytesPerSecond == 0 || got.Source != "conservative-default" {
		t.Fatalf("cold start degraded to an unlimited or unexplained limit: %+v", got)
	}
	if got.MeteredStatus != "unknown" {
		t.Fatalf("metered status %q, want unknown when no OS signal exists", got.MeteredStatus)
	}
}

// One active test at setup, then passive forever. A second active test is refused
// rather than silently re-measuring, because repeated active tests burn the
// metered data cap §16 warns about.
func TestSetupMeasurementRunsOnceAndFailsSafe(t *testing.T) {
	e, _ := NewEstimator(newFakeClock(), nil)
	measured := sample(4<<20, 0) // 4 MiB/s
	calls := 0
	m := func(context.Context) (Sample, error) { calls++; return measured, nil }
	if err := e.RunSetupMeasurement(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := e.Estimate()
	if want := clampRate(float64(4<<20) * HeadroomFraction); got.BytesPerSecond != want {
		t.Fatalf("setup limit %d, want %d (headroom applied)", got.BytesPerSecond, want)
	}
	if got.Source != "setup-measurement" {
		t.Fatalf("source %q, want setup-measurement", got.Source)
	}
	if err := e.RunSetupMeasurement(context.Background(), m); err == nil || calls != 1 {
		t.Fatal("a second active speed test was allowed")
	}

	failing, _ := NewEstimator(newFakeClock(), nil)
	if err := failing.RunSetupMeasurement(context.Background(),
		func(context.Context) (Sample, error) { return Sample{}, errors.New("no route") }); err == nil {
		t.Fatal("a failed measurement was accepted")
	}
	if got := failing.Estimate(); got.BytesPerSecond != ConservativeBytesPerSecond {
		t.Fatalf("failed measurement produced %d, want the conservative default", got.BytesPerSecond)
	}
	// An implausible result is a failure, not a fast link.
	if err := failing.RunSetupMeasurement(context.Background(),
		func(context.Context) (Sample, error) { return Sample{Bytes: 1 << 40, Elapsed: time.Millisecond}, nil }); err == nil {
		t.Fatal("an implausible measurement was accepted")
	}
	if got := failing.Estimate(); got.BytesPerSecond != ConservativeBytesPerSecond {
		t.Fatal("an implausible measurement changed the limit")
	}
}

// The statistic is a trimmed low percentile, not a mean: the mean hides the slow
// observations, which are exactly what saturation feels like to the owner.
func TestPassiveEstimateUsesTrimmedLowPercentile(t *testing.T) {
	e, _ := NewEstimator(newFakeClock(), nil)
	// Rates in bytes/s, deliberately out of order, with one absurd fast outlier.
	for _, rate := range []uint64{300_000, 100_000, 500_000, 10_000_000, 200_000, 400_000} {
		if err := e.Observe(sample(rate, 0)); err != nil {
			t.Fatal(err)
		}
	}
	// Trim drops 100_000 and 10_000_000, leaving 200k/300k/400k/500k; p25 by
	// nearest rank is 300_000; headroom then applies.
	want := clampRate(300_000 * HeadroomFraction)
	got := e.Estimate()
	if got.BytesPerSecond != want {
		t.Fatalf("passive limit %d, want %d", got.BytesPerSecond, want)
	}
	if got.Source != "passive" {
		t.Fatalf("source %q, want passive", got.Source)
	}
	// A mean would sit far above this; assert the direction explicitly so a
	// future refactor cannot quietly reintroduce one.
	if got.BytesPerSecond > clampRate(1_000_000*HeadroomFraction) {
		t.Fatal("estimate looks like a mean, not a low percentile")
	}
}

func TestImplausibleSamplesAreRejected(t *testing.T) {
	e, _ := NewEstimator(newFakeClock(), nil)
	for name, s := range map[string]Sample{
		"no elapsed time":                  {Bytes: 1 << 20},
		"too small":                        {Bytes: 1024, Elapsed: time.Second},
		"faster than any residential link": {Bytes: 1 << 40, Elapsed: time.Millisecond},
		"negative elapsed":                 {Bytes: 1 << 20, Elapsed: -time.Second},
	} {
		if err := e.Observe(s); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if got := e.Estimate(); got.Source != "conservative-default" {
		t.Fatal("rejected samples still moved the estimate")
	}
}

// Latency, not throughput, is the saturation signal: RTT climbs sharply before
// throughput visibly plateaus, and it is free to observe.
func TestLatencyRiseBacksOffAndRecoversSlowly(t *testing.T) {
	e, _ := NewEstimator(newFakeClock(), nil)
	for i := 0; i < 4; i++ {
		if err := e.Observe(sample(1_000_000, 20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	calm := e.Estimate()
	if calm.Saturated {
		t.Fatal("a steady 20 ms RTT was read as saturation")
	}
	if err := e.Observe(sample(1_000_000, 200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	loaded := e.Estimate()
	if !loaded.Saturated || loaded.BytesPerSecond >= calm.BytesPerSecond {
		t.Fatalf("latency rise did not back the shaper off: %+v", loaded)
	}
	if loaded.Reason == calm.Reason {
		t.Fatal("the backoff is invisible; §26 requires a visible reason")
	}
	// Recovery must be slower than backoff, or the shaper oscillates.
	before := loaded.BytesPerSecond
	if err := e.Observe(sample(1_000_000, 20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	recovered := e.Estimate()
	if recovered.Saturated {
		t.Fatal("a calm sample did not clear the saturation verdict")
	}
	if recovered.BytesPerSecond <= before {
		t.Fatal("no recovery after the link calmed down")
	}
	if recovered.BytesPerSecond >= calm.BytesPerSecond {
		t.Fatal("recovery was instant; multiplicative decrease must outpace increase")
	}
}

// Sustained saturation must still make progress: an unbounded decrease ends at
// zero throughput, and the floor keeps a backup crawling rather than stopping.
func TestSustainedSaturationStopsAtAFloor(t *testing.T) {
	e, _ := NewEstimator(newFakeClock(), nil)
	if err := e.Observe(sample(1_000_000, 10*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := e.Observe(sample(1_000_000, time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got := e.Estimate()
	floor := clampRate(1_000_000 * HeadroomFraction * minPenalty)
	if got.BytesPerSecond < floor || got.BytesPerSecond > floor+1 {
		t.Fatalf("saturated limit %d, want the penalty floor near %d", got.BytesPerSecond, floor)
	}
	if got.BytesPerSecond < MinBytesPerSecond {
		t.Fatal("backoff fell below the validated policy floor")
	}
}

// Metered: throttle hard, keep going. The ceiling is a maximum, so it never
// raises a slower estimate, and it applies even where the owner chose unlimited.
func TestMeteredCeilingCapsButNeverRaises(t *testing.T) {
	fast, _ := NewEstimator(newFakeClock(), fixedMetered{MeteredYes})
	for i := 0; i < 4; i++ {
		_ = fast.Observe(sample(10<<20, 0))
	}
	got := fast.Estimate()
	if got.BytesPerSecond != MeteredCeilingBytesPerSecond {
		t.Fatalf("metered limit %d, want the metered ceiling %d", got.BytesPerSecond, MeteredCeilingBytesPerSecond)
	}
	if got.BytesPerSecond == 0 {
		t.Fatal("a metered path was paused instead of throttled")
	}
	if got.Source != "metered-ceiling" || got.MeteredStatus != "metered" {
		t.Fatalf("metered decision is not visible: %+v", got)
	}

	slow, _ := NewEstimator(newFakeClock(), fixedMetered{MeteredYes})
	for i := 0; i < 4; i++ {
		_ = slow.Observe(sample(70_000, 0))
	}
	if limit := slow.Estimate().BytesPerSecond; limit > MeteredCeilingBytesPerSecond {
		t.Fatalf("metered ceiling raised a slower estimate to %d", limit)
	}

	unmetered, _ := NewEstimator(newFakeClock(), fixedMetered{MeteredNo})
	if got := unmetered.Estimate(); got.MeteredStatus != "unmetered" ||
		got.BytesPerSecond != ConservativeBytesPerSecond {
		t.Fatalf("a known-unmetered path was capped: %+v", got)
	}
}

// Evidence expires. An owner who moved house or switched to a hotspot is on a
// different link, and yesterday's observation of the old one must not govern it.
func TestStalePassiveSamplesExpire(t *testing.T) {
	clock := newFakeClock()
	e, _ := NewEstimator(clock, nil)
	for i := 0; i < 4; i++ {
		_ = e.Observe(sample(4<<20, 0))
	}
	if e.Estimate().Source != "passive" {
		t.Fatal("fresh samples were ignored")
	}
	clock.advance(sampleMaxAge + time.Minute)
	got := e.Estimate()
	if got.Source != "conservative-default" {
		t.Fatalf("stale samples still governed the limit: %+v", got)
	}
}

// The derived value is clamped into the same bounds config.ResourcePolicy
// validates, so a measurement can never produce a limit that fails to persist.
func TestEstimateStaysInsidePolicyBounds(t *testing.T) {
	e, _ := NewEstimator(newFakeClock(), nil)
	for i := 0; i < 4; i++ {
		_ = e.Observe(sample(minSampleBytes, 0))
	}
	if got := e.Estimate().BytesPerSecond; got < MinBytesPerSecond || got > MaxBytesPerSecond {
		t.Fatalf("estimate %d escaped the policy bounds", got)
	}
	if _, err := NewEstimator(nil, nil); err == nil {
		t.Error("an estimator without a clock was accepted")
	}
}
