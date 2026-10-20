package remoteinference

import (
	"testing"
	"time"
)

func TestWakeWatchDetectsSleepGap(t *testing.T) {
	var w wakeWatch
	t0 := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	if w.tick(t0, 4*time.Second, time.Minute) {
		t.Fatal("first tick is not a wake")
	}
	if w.tick(t0.Add(4*time.Second), 4*time.Second, time.Minute) || w.settling(t0.Add(5*time.Second)) {
		t.Fatal("normal cadence is not a wake")
	}
	if !w.tick(t0.Add(10*time.Minute), 4*time.Second, time.Minute) {
		t.Fatal("a 10 minute gap is a wake")
	}
	if !w.settling(t0.Add(10*time.Minute + 30*time.Second)) {
		t.Fatal("should be settling after wake")
	}
	if w.settling(t0.Add(12 * time.Minute)) {
		t.Fatal("settling should end")
	}
}

func TestPersistNeedsContinuousBadness(t *testing.T) {
	var p persist
	t0 := time.Now()
	if p.bad(t0, 15*time.Second) {
		t.Fatal("first sighting is not enough")
	}
	p.ok()
	if p.bad(t0.Add(20*time.Second), 15*time.Second) {
		t.Fatal("reset must restart the clock")
	}
	if !p.bad(t0.Add(40*time.Second), 15*time.Second) {
		t.Fatal("bad for 20s should trip")
	}
}

func TestPoolBackoffGrowsAndCaps(t *testing.T) {
	if poolBackoff(1) != 30*time.Second || poolBackoff(3) != 90*time.Second || poolBackoff(50) != 5*time.Minute {
		t.Fatal("backoff schedule wrong")
	}
}

func TestRejoinDelayBacksOffAfterRepeatedCrashes(t *testing.T) {
	r := &Runner{Rejoin: 5 * time.Second}
	n := 0
	if r.rejoinDelay(&n, false, time.Minute) != 5*time.Second {
		t.Fatal("a ring change rejoins at the normal pace")
	}
	got := []time.Duration{r.rejoinDelay(&n, true, time.Second), r.rejoinDelay(&n, true, time.Second), r.rejoinDelay(&n, true, time.Second)}
	if got[0] != 5*time.Second || got[1] != 10*time.Second || got[2] != 20*time.Second {
		t.Fatal(got)
	}
	for i := 0; i < 10; i++ {
		r.rejoinDelay(&n, true, time.Second)
	}
	if r.rejoinDelay(&n, true, time.Second) != 2*time.Minute {
		t.Fatal("capped at two minutes")
	}
	if r.rejoinDelay(&n, true, 10*time.Minute) != 5*time.Second {
		t.Fatal("a long healthy run resets the count")
	}
}
