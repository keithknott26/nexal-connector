package throttle

import (
	"sync"
	"time"
)

// fakeClock is why this package takes a Clock. Asserting an achieved byte rate
// against the wall clock means either a multi-second test or a flaky one, and a
// shaper whose arithmetic is never asserted is decoration. Virtual time makes the
// assertions exact: 1000 bytes at 1000 B/s with a 100-byte burst must advance the
// clock by exactly 900 ms, not "about a second".
type fakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []*fakeWaiter
	stopped bool
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	c := &fakeClock{now: time.Unix(1_600_000_000, 0)}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer registers a waiter and returns immediately. The channel is buffered
// so firing it can never block the advancing goroutine, which is exactly the
// deadlock a naive fake clock introduces. Stopping removes the waiter, which is
// how the cancellation test proves the limiter does not leak timers.
func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := &fakeWaiter{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		w.ch <- c.now
		return w.ch, func() {}
	}
	c.waiters = append(c.waiters, w)
	c.cond.Broadcast()
	return w.ch, func() { c.remove(w) }
}

func (c *fakeClock) remove(target *fakeWaiter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := c.waiters[:0]
	for _, w := range c.waiters {
		if w != target {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining
}

func (c *fakeClock) waiterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// waitForWaiter blocks until someone is actually waiting on the clock, so a test
// advances time only after the code under test has committed to sleeping. Without
// it the test races the goroutine it is driving. Reports false once stopped.
func (c *fakeClock) waitForWaiter() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.waiters) == 0 && !c.stopped {
		c.cond.Wait()
	}
	return len(c.waiters) > 0
}

// advanceToEarliest jumps to the first pending deadline and fires everything due,
// which keeps virtual time equal to the shaping delay the limiter asked for.
func (c *fakeClock) advanceToEarliest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	earliest := time.Time{}
	for _, w := range c.waiters {
		if earliest.IsZero() || w.at.Before(earliest) {
			earliest = w.at
		}
	}
	if earliest.IsZero() {
		return
	}
	c.now = earliest
	c.fireLocked()
}

// advance moves virtual time by d, used where the test cares about elapsed time
// rather than about unblocking a specific waiter (sample expiry, for instance).
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.fireLocked()
}

func (c *fakeClock) fireLocked() {
	remaining := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		remaining = append(remaining, w)
	}
	c.waiters = remaining
}

// stop releases a pump goroutine. A test that finishes while a pump is parked in
// cond.Wait would otherwise leak it past the test's lifetime.
func (c *fakeClock) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	c.cond.Broadcast()
}

// pump drives virtual time for tests that cannot count the waits in advance,
// such as the concurrent one.
func (c *fakeClock) pump() {
	for c.waitForWaiter() {
		c.advanceToEarliest()
	}
}
