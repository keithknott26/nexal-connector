package sandbox

import (
	"context"
	"testing"
)

func TestThrottleDropsNoise(t *testing.T) {
	var got []string
	f := throttle(func(s string, p int) { got = append(got, s) })
	f(StepDownload, 5)
	f(StepDownload, 6) // under 3 points: dropped
	f(StepDownload, 9) // within 2 s of the last report: dropped
	f(StepConvert, 60) // new step: always sent
	if len(got) != 2 || got[0] != StepDownload || got[1] != StepConvert {
		t.Fatalf("got %v", got)
	}
}

func TestProgressOfDefaultsToNoop(t *testing.T) {
	progressOf(context.Background())("x", 1) // must not panic
	var nilFn ProgressFunc
	progressOf(WithProgress(context.Background(), nilFn))("x", 1)
}

func TestWithProgressRoundTrip(t *testing.T) {
	var step string
	var pct int
	ctx := WithProgress(context.Background(), func(s string, p int) { step, pct = s, p })
	progressOf(ctx)(StepSeed, 42)
	if step != StepSeed || pct != 42 {
		t.Fatalf("got %q %d", step, pct)
	}
}

func TestThrottleSendsAfterBigJumpOnlyWhenIntervalElapsed(t *testing.T) {
	n := 0
	f := throttle(func(string, int) { n++ })
	f(StepDownload, 0)
	f(StepDownload, 50) // big jump but inside 2 s: dropped
	if n != 1 {
		t.Fatalf("n=%d", n)
	}
}

func TestReportProgressGuardsAndSends(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, &fakeGuest{})
	m.reportProgress("sb1", StepBoot, 10) // no coordinator: no-op
	c := &recCoord{}
	m.BindCoordinator(c)
	m.reportProgress("sb1", StepBoot, 10) // no host id: no-op
	if len(c.all()) != 0 {
		t.Fatalf("unexpected reports %+v", c.all())
	}
	m.mu.Lock()
	m.hostID = "h1"
	m.ctx = context.Background()
	m.mu.Unlock()
	m.reportProgress("sb1", StepBoot, 10)
	reps := c.all()
	if len(reps) != 1 || reps[0].SandboxID != "sb1" || reps[0].State != StateProvisioning || reps[0].Step != StepBoot || reps[0].Percent != 10 {
		t.Fatalf("%+v", reps)
	}
}
