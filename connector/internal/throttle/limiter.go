// Package throttle bounds how fast this connector is allowed to push bytes at
// the owner's uplink, and derives that bound from observation rather than from a
// number typed into a field.
//
// HONESTY: nothing in the connector uploads bulk data today, so nothing is
// throttled today. internal/bundletransfer moves a four-file enrollment bundle
// capped at MaxPlaintext (6 KB of ciphertext), which is not a bandwidth problem
// and is deliberately left unwrapped. The Time Machine / JuiceFS storage path
// that WOULD saturate a home uplink for hours is gated behind HARDENING-PLAN
// §21 Step 0 (an untested 200 GB backup and timed restore) and does not exist in
// this repository. This package is the mechanism, the policy and the measurement
// built and tested ahead of that path, so the path arrives with a shaper already
// reviewed instead of shipping unshaped and being retrofitted. Every status and
// report surface says "not enforced" for exactly that reason.
//
// It is also only HALF of HARDENING-PLAN §16's requirement, and deliberately stays
// that half. §16 demands "a per-donor monthly bandwidth budget, visible in the
// connector UI, with cap-aware scheduling that throttles repair and relay work as a
// donor approaches their limit". A bytes-per-second ceiling cannot bound a monthly
// volume — a permanent 64 KiB/s metered ceiling still moves ~165 GB in a month — so
// the VOLUME half now lives in internal/budget, which persists its rolling window in
// the coordinator so it survives a restart. This package holds only the seam (see
// meter.go): the Meter interface plus NewMeteredWriter/NewMeteredReader. The
// dependency points one way, budget -> throttle, because the rate shaper must not
// need to know how a volume budget is persisted.
package throttle

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Clock is the only time source this package reads. It exists so the limiter can
// be tested against a fake clock: asserting an achieved byte rate with wall-clock
// sleeps means either a multi-second test or a flaky one, and a shaper whose
// arithmetic is never asserted is not a shaper.
type Clock interface {
	Now() time.Time
	// NewTimer must deliver exactly once after d has elapsed on this clock and
	// must not block the caller, so implementations return a buffered channel.
	// The returned stop function releases the timer: a shaping delay on a slow
	// uplink can be seconds long, and a transfer cancelled mid-wait would
	// otherwise leave one armed timer per abandoned chunk.
	NewTimer(d time.Duration) (<-chan time.Time, func())
}

// SystemClock is the production Clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }
func (SystemClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// Limiter is a hand-rolled token bucket. Zero dependencies is a hard constraint
// here, so golang.org/x/time/rate is deliberately not used; the reservation model
// below is the same idea reduced to what a byte shaper needs.
//
// Concurrency: all state is guarded by mu and every wait happens OUTSIDE the
// lock, so one blocked transfer cannot stall another's accounting. There is no
// polling anywhere — a caller that must wait blocks on one timer and on its
// context, which is why a cancelled transfer returns immediately instead of at
// the end of some tick.
type Limiter struct {
	mu    sync.Mutex
	rate  float64 // bytes per second; 0 means unlimited
	burst int64   // bucket capacity in bytes, also the largest single reservation
	// tokens may go negative: a reservation that cannot be served now is still
	// granted, and the caller waits out the deficit it created. That is what
	// serializes concurrent writers fairly instead of having them all wake,
	// re-check and race (a thundering herd that also burns CPU).
	tokens float64
	last   time.Time
	clock  Clock
}

// NewLimiter builds a full bucket: the first burst bytes of a transfer go out
// immediately, which keeps small writes from paying a shaping delay they cannot
// amortise. bytesPerSecond == 0 means unlimited and is a valid, explicit state,
// not a bug — config.ResourcePolicy has an "unlimited" mode.
func NewLimiter(bytesPerSecond uint64, burst int64, clock Clock) (*Limiter, error) {
	if burst <= 0 {
		return nil, errors.New("token bucket burst must be positive")
	}
	if clock == nil {
		return nil, errors.New("token bucket requires a clock")
	}
	return &Limiter{rate: float64(bytesPerSecond), burst: burst,
		tokens: float64(burst), last: clock.Now(), clock: clock}, nil
}

// Burst is the largest single reservation the limiter will serve.
func (l *Limiter) Burst() int64 { return l.burst }

// SetRate changes the ceiling on a live limiter. The measured limit moves when
// the estimator learns something new and when the owner changes policy, and an
// in-flight multi-hour upload must pick that up without being torn down.
// Outstanding deficits are kept: they were already promised to waiting callers,
// and re-basing them would hand out bandwidth the old policy had spent.
func (l *Limiter) SetRate(bytesPerSecond uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refillLocked()
	l.rate = float64(bytesPerSecond)
}

func (l *Limiter) refillLocked() {
	now := l.clock.Now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.rate
		if l.tokens > float64(l.burst) {
			l.tokens = float64(l.burst)
		}
		l.last = now
	}
}

// reserve consumes n tokens and reports how long the caller must wait before
// they are actually earned.
func (l *Limiter) reserve(n int64) (time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > l.burst {
		// Callers chunk to Burst(); a larger reservation would be an unbounded
		// sleep, so it is a programming error rather than a slow path.
		return 0, errors.New("reservation exceeds token bucket burst")
	}
	if l.rate <= 0 {
		return 0, nil // unlimited: no accounting to keep
	}
	l.refillLocked()
	l.tokens -= float64(n)
	if l.tokens >= 0 {
		return 0, nil
	}
	return time.Duration(-l.tokens / l.rate * float64(time.Second)), nil
}

// Wait blocks until n bytes may be written, or until ctx ends.
//
// A cancelled wait does NOT return its tokens. The bias is deliberate: an
// abandoned chunk briefly makes the limiter stricter than the policy, and
// erring toward using less of the owner's uplink is the safe direction for a
// feature whose whole purpose is not saturating their network.
func (l *Limiter) Wait(ctx context.Context, n int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n <= 0 {
		return nil
	}
	delay, err := l.reserve(n)
	if err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer, stop := l.clock.NewTimer(delay)
	defer stop()
	select {
	case <-timer:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Writer shapes writes to an underlying stream. The context lives in the struct
// because io.Writer has no context parameter and a shaped upload must still be
// cancellable; construct one per transfer with that transfer's context.
type Writer struct {
	w       io.Writer
	limiter *Limiter
	ctx     context.Context
	// meter is the §16 monthly VOLUME budget (meter.go), nil for the pre-budget
	// constructors. Rate and volume are different limits and a shaper that enforces
	// only the first still walks a donor into a 1.2 TB ISP cap.
	meter Meter
}

func NewWriter(ctx context.Context, w io.Writer, l *Limiter) (*Writer, error) {
	if ctx == nil || w == nil || l == nil {
		return nil, errors.New("shaped writer requires a context, a writer and a limiter")
	}
	return &Writer{w: w, limiter: l, ctx: ctx}, nil
}

// Write pays for each chunk before sending it, never after: charging afterwards
// would let a single large write put the full payload on the wire and only then
// notice it had exceeded the ceiling.
func (s *Writer) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		chunk := int64(len(p) - written)
		if chunk > s.limiter.Burst() {
			chunk = s.limiter.Burst()
		}
		// Volume before rate: an exhausted budget must fail now, not after sleeping out
		// a shaping delay for bytes the donor is not allowed to send. Charging per chunk
		// rather than per Write also means a refusal costs at most one burst of overrun.
		if err := charge(s.meter, chunk); err != nil {
			return written, err
		}
		if err := s.limiter.Wait(s.ctx, chunk); err != nil {
			return written, err
		}
		n, err := s.w.Write(p[written : written+int(chunk)])
		written += n
		if err != nil {
			return written, err
		}
		if int64(n) != chunk {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

// Reader shapes reads for the symmetric case: a restore fanning in, or a local
// source feeding an upload whose sink does not go through Writer.
type Reader struct {
	r       io.Reader
	limiter *Limiter
	ctx     context.Context
	// See Writer.meter. Inbound bytes count against a residential cap too.
	meter Meter
}

func NewReader(ctx context.Context, r io.Reader, l *Limiter) (*Reader, error) {
	if ctx == nil || r == nil || l == nil {
		return nil, errors.New("shaped reader requires a context, a reader and a limiter")
	}
	return &Reader{r: r, limiter: l, ctx: ctx}, nil
}

// Read pays for at most one burst per call and short-reads rather than looping,
// so a caller with a large buffer still observes progress at the shaped rate.
func (s *Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if int64(len(p)) > s.limiter.Burst() {
		p = p[:s.limiter.Burst()]
	}
	// Charged for the bytes we are about to ASK for, which is the only number known
	// before the read. A short read therefore over-charges by the difference, and that is
	// the deliberate direction: over-charging costs the donor a little speed, while
	// under-charging costs them money.
	if err := charge(s.meter, int64(len(p))); err != nil {
		return 0, err
	}
	if err := s.limiter.Wait(s.ctx, int64(len(p))); err != nil {
		return 0, err
	}
	return s.r.Read(p)
}
