package throttle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The achieved rate is the whole contract. A full bucket lets the first burst
// through free, and every later chunk costs burst/rate of virtual time, so 1000
// bytes at 1000 B/s with a 100-byte burst must take exactly 900 ms.
func TestShapedWriterAchievesConfiguredRate(t *testing.T) {
	clock := newFakeClock()
	limiter, err := NewLimiter(1000, 100, clock)
	if err != nil {
		t.Fatal(err)
	}
	var sink bytes.Buffer
	w, err := NewWriter(context.Background(), &sink, limiter)
	if err != nil {
		t.Fatal(err)
	}
	start := clock.Now()
	done := make(chan error, 1)
	go func() {
		_, err := w.Write(bytes.Repeat([]byte("x"), 1000))
		done <- err
	}()
	// Nine waits: ten chunks, the first paid for by the initial burst.
	for i := 0; i < 9; i++ {
		if !clock.waitForWaiter() {
			t.Fatal("limiter stopped waiting before the payload was shaped")
		}
		clock.advanceToEarliest()
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sink.Len() != 1000 {
		t.Fatalf("wrote %d bytes, want 1000", sink.Len())
	}
	if elapsed := clock.Now().Sub(start); elapsed != 900*time.Millisecond {
		t.Fatalf("shaping took %v of virtual time, want 900ms", elapsed)
	}
}

// Burst exists so small writes do not pay a shaping delay they cannot amortise:
// a write that fits the bucket must not touch the clock at all.
func TestBurstIsNotDelayedAndIsSpentOnce(t *testing.T) {
	clock := newFakeClock()
	limiter, _ := NewLimiter(1000, 100, clock)
	w, _ := NewWriter(context.Background(), io.Discard, limiter)
	if _, err := w.Write(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if clock.waiterCount() != 0 {
		t.Fatal("a write inside the burst waited on the clock")
	}
	// The bucket is now empty, so the next byte must wait rather than being
	// granted a second free burst.
	go func() { _, _ = w.Write(make([]byte, 1)) }()
	if !clock.waitForWaiter() {
		t.Fatal("a write past the burst was not delayed")
	}
}

// An hours-long Time Machine upload must abort when the owner pauses, and abort
// now: a limiter that only notices cancellation at the end of its sleep would
// hold a paused transfer open for the remainder of its shaping delay.
func TestCancellationReturnsWithoutWaitingOutTheDelay(t *testing.T) {
	clock := newFakeClock()
	limiter, _ := NewLimiter(1000, 100, clock)
	ctx, cancel := context.WithCancel(context.Background())
	w, _ := NewWriter(ctx, io.Discard, limiter)
	if _, err := w.Write(make([]byte, 100)); err != nil { // spend the burst
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.Write(make([]byte, 100))
		done <- err
	}()
	if !clock.waitForWaiter() {
		t.Fatal("the second write did not block")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled write returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		// Virtual time never advanced, so a pass here means the limiter woke on
		// the context rather than on its timer.
		t.Fatal("cancelled write did not return promptly")
	}
	if clock.waiterCount() != 0 {
		t.Fatal("a cancelled wait left a timer registered")
	}
}

// Concurrent shaped streams share one uplink, so they share one limiter. Under
// -race this also asserts the accounting is not merely lucky.
func TestConcurrentWritersShareOneLimiter(t *testing.T) {
	clock := newFakeClock()
	limiter, _ := NewLimiter(4096, 64, clock)
	go clock.pump()
	defer clock.stop()
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var sink bytes.Buffer
			w, err := NewWriter(context.Background(), &sink, limiter)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := w.Write(make([]byte, 512)); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			total += sink.Len()
			mu.Unlock()
		}()
	}
	// A live policy change mid-transfer must not corrupt the accounting either.
	limiter.SetRate(8192)
	wg.Wait()
	if total != 8*512 {
		t.Fatalf("shaped %d bytes across writers, want %d", total, 8*512)
	}
}

// Unlimited is an explicit mode, not an accident, and it must cost nothing: no
// timers, no accounting, no clock reads that could block.
func TestUnlimitedRateNeverWaits(t *testing.T) {
	clock := newFakeClock()
	limiter, _ := NewLimiter(0, 64, clock)
	w, _ := NewWriter(context.Background(), io.Discard, limiter)
	if _, err := w.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if clock.waiterCount() != 0 {
		t.Fatal("unlimited mode shaped a write")
	}
}

// The reader is the symmetric half: it short-reads at the burst so a caller with
// a large buffer still observes progress at the shaped rate instead of stalling.
func TestShapedReaderPaysPerBurst(t *testing.T) {
	clock := newFakeClock()
	limiter, _ := NewLimiter(1000, 100, clock)
	r, err := NewReader(context.Background(), strings.NewReader(strings.Repeat("y", 250)), limiter)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 250)
	n, err := r.Read(buf)
	if err != nil || n != 100 {
		t.Fatalf("read %d bytes (%v), want a 100-byte burst-sized read", n, err)
	}
	if clock.waiterCount() != 0 {
		t.Fatal("the first read inside the burst waited")
	}
}

func TestLimiterRejectsUnusableConfiguration(t *testing.T) {
	clock := newFakeClock()
	if _, err := NewLimiter(1000, 0, clock); err == nil {
		t.Error("a zero burst was accepted; every write would sleep forever")
	}
	if _, err := NewLimiter(1000, 64, nil); err == nil {
		t.Error("a limiter without a clock was accepted")
	}
	limiter, _ := NewLimiter(1000, 64, clock)
	if _, err := limiter.reserve(65); err == nil {
		t.Error("a reservation larger than the burst was accepted")
	}
	if _, err := NewWriter(context.Background(), nil, limiter); err == nil {
		t.Error("a writer without a sink was accepted")
	}
	if _, err := NewReader(nil, strings.NewReader(""), limiter); err == nil {
		t.Error("a reader without a context was accepted")
	}
}
