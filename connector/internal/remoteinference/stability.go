package remoteinference

import "time"

// Pool stability helpers. A laptop that sleeps freezes its own timers while the coordinator's lease on it
// keeps running, so on waking, every Mac's view of the ring is briefly wrong. A short coordinator hiccup looks
// the same. Neither should tear a working pool down, and a pool that keeps failing should back off.

const (
	probeFailLimit = 3                // consecutive failed probes before an idle pool is declared broken
	stableAfter    = 5 * time.Minute  // a pool that ran this long before failing is not counted as flapping
	minWakeGap     = 15 * time.Second // the smallest gap between two ticks that counts as a sleep
)

// wakeWatch notices that this Mac slept: the wall clock jumps while ticks were expected every poll.
type wakeWatch struct {
	last  time.Time
	until time.Time
}

// tick records one loop iteration and reports whether the Mac just woke from sleep. After a wake the
// watch stays settling for grace, so ring membership and stalls are not judged on stale clocks.
func (w *wakeWatch) tick(now time.Time, poll, grace time.Duration) bool {
	now = now.Round(0) // drop the monotonic reading, which does not advance during sleep
	gap := 3 * poll
	if gap < minWakeGap {
		gap = minWakeGap
	}
	woke := !w.last.IsZero() && now.Sub(w.last) > gap
	w.last = now
	if woke {
		w.until = now.Add(grace)
	}
	return woke
}

func (w *wakeWatch) settling(now time.Time) bool { return now.Round(0).Before(w.until) }

// persist reports a condition only once it has held continuously for a while.
type persist struct{ since time.Time }

func (p *persist) bad(now time.Time, d time.Duration) bool {
	if p.since.IsZero() {
		p.since = now
	}
	return now.Sub(p.since) >= d
}

func (p *persist) ok() { p.since = time.Time{} }

// poolBackoff is how long to wait before the nth failed attempt to run a pool is tried again.
func poolBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	wait := time.Duration(failures) * 30 * time.Second
	if wait > 5*time.Minute {
		wait = 5 * time.Minute
	}
	return wait
}
