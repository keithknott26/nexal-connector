package throttle

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Bounds and policy constants. Every number here is derived from something
// written down, because a silently invented throttle value is the kind of
// number nobody can review later.
//
// HARDENING-PLAN §16 ("Donor upstream is the real constraint") tabulates the
// residential links this has to survive: cable/DSL upstream 10–35 Mb/s,
// symmetric fibre 300–1000 Mb/s.
const (
	// MinBytesPerSecond is the floor. Below roughly a quarter megabit nothing
	// completes within a lease and the shaper is indistinguishable from broken,
	// so this is the lowest value the owner or the estimator may select.
	MinBytesPerSecond = 32 << 10 // 32 KiB/s ≈ 0.26 Mb/s
	// MaxBytesPerSecond sits above §16's fastest documented residential row
	// (1000 Mb/s ≈ 125 MB/s). A measurement above this is not a fast link, it is
	// an artifact — a loopback test, a proxy absorbing the bytes, or a bad clock —
	// and is discarded rather than trusted.
	MaxBytesPerSecond = 1 << 30 // 1 GiB/s ≈ 8.6 Gb/s
	// ConservativeBytesPerSecond is the cold-start and fallback value: half of
	// §16's SLOWEST documented upstream (10 Mb/s), because before any
	// measurement exists the only safe assumption is the worst link in the
	// table. Fallback is never "unlimited"; an unmeasured link is exactly the
	// case where saturating it is most likely.
	ConservativeBytesPerSecond = 512 << 10 // 512 KiB/s ≈ 4.2 Mb/s
	// MeteredCeilingBytesPerSecond applies when the OS reports the path as
	// metered/expensive. The founder decision is throttle hard but KEEP GOING —
	// a hotspot may be the owner's only link, so pausing is worse than crawling.
	//
	// Caveat, stated rather than hidden: a rate ceiling does not bound a monthly
	// volume. 64 KiB/s sustained is still ~165 GB/month. The §16 monthly budget
	// is what actually protects a data cap and it does not exist yet.
	MeteredCeilingBytesPerSecond = 64 << 10 // 64 KiB/s ≈ 0.5 Mb/s
	// DefaultBurstBytes is one token-bucket burst. 64 KiB matches the JuiceFS
	// block size named in §17 (4 MB blocks are composed of smaller writes) and is
	// small enough that a burst cannot itself spike a slow uplink's queue.
	DefaultBurstBytes = 64 << 10
)

// SamplePercentile is the statistic applied to the passive sample window.
//
// The mean is explicitly rejected: it hides the slow observations, and one
// sample taken while the uplink was already busy would drag an average down and
// keep it down. A LOW percentile is the right shape for a shaper — we want the
// rate the link sustains on a bad-but-normal day, not its best moment. p25 over
// a trimmed window is the choice: the median still tracks a link that is idle
// half the time, and p25 biases toward the congested half of the distribution,
// which is when the owner notices us.
const SamplePercentile = 25

// HeadroomFraction is applied AFTER the percentile. Shaping practice for
// bufferbloat is to set the shaper below the true line rate so the queue forms
// in software where it can be managed rather than in the modem: OpenWrt's SQM
// documentation and every CAKE guide start at 85–95% of measured throughput
// (https://openwrt.org/docs/guide-user/network/traffic-shaping/sqm-details).
//
// 0.7 is below that band on purpose, and the two factors are separate so neither
// is double-counted: the low percentile already accounts for sharing the link
// with the household, and this factor is only the anti-saturation margin. It is
// a product decision, reviewable in one place, not an arbitrary constant.
const HeadroomFraction = 0.7

// Saturation is detected from latency, not throughput: when a link saturates,
// RTT climbs sharply well before throughput visibly plateaus, and a rising RTT
// costs nothing to observe because it rides on transfers already happening.
// The baseline is the minimum RTT seen in the window, which is the same
// windowed-min-RTT signal BBR uses as its propagation-delay estimate
// (https://www.ietf.org/archive/id/draft-ietf-ccwg-bbr-04.html).
const (
	// SaturationRTTMultiple: 2× the windowed minimum. Well outside normal
	// residential jitter, well inside the order-of-magnitude rises bufferbloat
	// produces, so this fires on real queueing rather than on noise.
	SaturationRTTMultiple = 2.0
	// Multiplicative decrease, additive-ish recovery — the asymmetry of every
	// congestion controller since AIMD. Backing off must be fast because the
	// owner is feeling it now; recovery must be slow because probing upward is
	// what caused the problem.
	saturationBackoff  = 0.8
	saturationRecovery = 1.05
	// A floor on the penalty: even a persistently saturated link must keep
	// making progress, and an unbounded decrease ends at zero throughput.
	minPenalty = 0.25
	// sampleWindow is small enough to follow a link that genuinely changed
	// (someone started a video call, the owner moved to a hotspot) and large
	// enough that one outlier cannot decide the percentile.
	sampleWindow = 16
	// minSamples: below three observations a percentile is theatre, so the setup
	// measurement is used instead.
	minSamples = 3
	// minSampleBytes: transfers smaller than this are dominated by connection
	// setup and round-trip latency, so their implied rate says nothing about
	// link capacity.
	minSampleBytes = 64 << 10
	// sampleMaxAge expires evidence. An owner who moved house, switched to a
	// hotspot or changed plan is on a different link, and a month-old
	// observation of the old one must not govern the new one forever.
	sampleMaxAge = 24 * time.Hour
)

// MeteredState is what the OS reports about the current path. Unknown is a
// first-class state and is NEVER treated as a measurement: guessing "metered"
// would crawl for every owner on a normal link, and guessing "unmetered" would
// bill someone. Unknown means the metered ceiling does not apply and the reason
// says so.
type MeteredState int

const (
	MeteredUnknown MeteredState = iota
	MeteredNo
	MeteredYes
)

func (m MeteredState) String() string {
	switch m {
	case MeteredNo:
		return "unmetered"
	case MeteredYes:
		return "metered"
	default:
		return "unknown"
	}
}

// MeteredSource reports path cost. There is NO implementation of this in the
// repository, and that is a deliberate gap rather than an oversight: the only
// reliable macOS signal is NWPathMonitor's isExpensive/isConstrained, which is
// Swift/Network.framework and cannot be built or tested in the Go sandbox (the
// macOS target compiles only in CI). The Go side is the interface, the policy
// and the ceiling; the Swift bridge that feeds it is a TODO, and until it exists
// every surface reports metered status as "unknown".
type MeteredSource interface {
	Metered() MeteredState
}

// Sample is one throughput observation. RTT is optional (zero means unknown) so
// a source that cannot measure latency still contributes to the percentile.
type Sample struct {
	Bytes   uint64
	Elapsed time.Duration
	RTT     time.Duration
}

// BytesPerSecond validates as it converts. Implausible samples are rejected at
// the door so no downstream statistic has to defend itself against them.
func (s Sample) BytesPerSecond() (float64, error) {
	if s.Elapsed <= 0 {
		return 0, errors.New("sample has no elapsed time")
	}
	if s.Bytes < minSampleBytes {
		return 0, errors.New("sample too small to imply a link rate")
	}
	rate := float64(s.Bytes) / s.Elapsed.Seconds()
	if rate <= 0 || rate > MaxBytesPerSecond {
		return 0, errors.New("implausible sample rate")
	}
	return rate, nil
}

// Measurer performs ONE active speed test. It is injectable so the estimator is
// testable offline with no network at all, and because the real implementation
// belongs to the bulk upload path that does not exist yet.
type Measurer func(ctx context.Context) (Sample, error)

// Estimator derives an automatic upload ceiling.
//
// Measurement is HYBRID by founder decision: exactly ONE active speed test at
// setup to solve cold start, then PASSIVE observation forever. There is no
// periodic active test, because an active test burns real upload bytes and a
// metered data cap every cycle — the precise harm §16 warns about, where a donor
// billed by their own ISP is a lost donor and bad press. Passive samples come
// from transfers that were going to happen anyway and cost nothing.
//
// Safe under concurrent use: an uploader reporting samples and a status reader
// asking for the current limit are different goroutines.
type Estimator struct {
	mu      sync.Mutex
	samples []observation // ring, newest appended, oldest dropped
	setup   float64       // bytes/s from the one setup measurement; 0 = never ran
	penalty float64       // saturation multiplier, (minPenalty, 1]
	minRTT  time.Duration
	metered MeteredSource
	clock   Clock
	// saturated records the most recent latency verdict so the reason surfaced
	// in the UI (§26) says why the ceiling moved, not just that it did.
	saturated bool
}

// observation is a sample plus when it was taken, so stale evidence can expire.
type observation struct {
	sample Sample
	at     time.Time
}

func NewEstimator(clock Clock, metered MeteredSource) (*Estimator, error) {
	if clock == nil {
		return nil, errors.New("estimator requires a clock")
	}
	return &Estimator{penalty: 1, metered: metered, clock: clock}, nil
}

// RunSetupMeasurement performs the single cold-start active test. Failure is not
// fatal and is not an excuse to go unlimited: the conservative default stands.
// Calling it twice is refused rather than silently re-measuring, because "one
// active test, at setup" is the whole point.
func (e *Estimator) RunSetupMeasurement(ctx context.Context, m Measurer) error {
	if m == nil {
		return errors.New("setup measurement requires a measurer")
	}
	e.mu.Lock()
	already := e.setup > 0
	e.mu.Unlock()
	if already {
		return errors.New("setup measurement already recorded; steady state is passive")
	}
	sample, err := m(ctx)
	if err != nil {
		return err
	}
	rate, err := sample.BytesPerSecond()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setup = rate
	return nil
}

// Observe records a passive sample taken from a transfer that already happened.
// An invalid sample is reported and dropped; it never perturbs the window.
func (e *Estimator) Observe(s Sample) error {
	if _, err := s.BytesPerSecond(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.samples = append(e.samples, observation{sample: s, at: e.clock.Now()})
	if len(e.samples) > sampleWindow {
		e.samples = e.samples[len(e.samples)-sampleWindow:]
	}
	if s.RTT > 0 {
		if e.minRTT == 0 || s.RTT < e.minRTT {
			e.minRTT = s.RTT
		}
		// Latency verdict, per sample: compare against the windowed minimum,
		// which is the closest thing available to an unqueued baseline.
		e.saturated = e.minRTT > 0 && float64(s.RTT) > SaturationRTTMultiple*float64(e.minRTT)
		if e.saturated {
			e.penalty *= saturationBackoff
			if e.penalty < minPenalty {
				e.penalty = minPenalty
			}
		} else if e.penalty < 1 {
			e.penalty *= saturationRecovery
			if e.penalty > 1 {
				e.penalty = 1
			}
		}
	}
	return nil
}

// Estimate is the derived automatic ceiling plus the reason for it. Source and
// Reason exist so §26's "a user can always see why" applies to bandwidth too,
// and so a support conversation does not start with a mystery number.
type Estimate struct {
	BytesPerSecond uint64       `json:"bytesPerSecond"`
	Source         string       `json:"source"`
	Metered        MeteredState `json:"-"`
	MeteredStatus  string       `json:"meteredStatus"`
	Saturated      bool         `json:"saturated"`
	Reason         string       `json:"reason"`
}

// Estimate resolves the current automatic limit. Precedence is passive window,
// then the setup measurement, then the conservative default — newest real
// evidence first, and never unlimited.
func (e *Estimator) Estimate() Estimate {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Estimate{Saturated: e.saturated}
	fresh := make([]Sample, 0, len(e.samples))
	cutoff := e.clock.Now().Add(-sampleMaxAge)
	for _, o := range e.samples {
		if o.at.After(cutoff) {
			fresh = append(fresh, o.sample)
		}
	}
	var raw float64
	// headroom is 1 for the conservative default because that constant is ALREADY
	// half of the slowest documented uplink; applying the shaping margin twice
	// would quietly land near the floor for every unmeasured host.
	headroom := HeadroomFraction
	if len(fresh) >= minSamples {
		raw = trimmedPercentile(fresh, SamplePercentile)
		out.Source = "passive"
		out.Reason = "derived from observed transfers"
	}
	if raw <= 0 && e.setup > 0 {
		raw = e.setup
		out.Source = "setup-measurement"
		out.Reason = "one-off setup speed test; awaiting passive samples"
	}
	if raw <= 0 {
		raw, headroom = ConservativeBytesPerSecond, 1
		out.Source = "conservative-default"
		out.Reason = "no measurement yet; conservative default from the slowest documented residential uplink"
	}
	limit := raw * headroom * e.penalty
	if e.saturated {
		out.Reason += "; backed off after a latency rise indicating saturation"
	}
	bps := clampRate(limit)
	metered := MeteredUnknown
	if e.metered != nil {
		metered = e.metered.Metered()
	}
	out.Metered, out.MeteredStatus = metered, metered.String()
	switch metered {
	case MeteredYes:
		if bps > MeteredCeilingBytesPerSecond {
			bps = MeteredCeilingBytesPerSecond
			out.Source = "metered-ceiling"
			out.Reason = "metered connection: hard ceiling, transfers continue slowly rather than pausing"
		}
	case MeteredUnknown:
		out.Reason += "; metered status unknown (no macOS NWPathMonitor bridge yet), so no metered ceiling applied"
	}
	out.BytesPerSecond = bps
	return out
}

// clampRate keeps every derived value inside the validated policy bounds, so a
// measurement can never produce a limit the config layer would refuse.
func clampRate(v float64) uint64 {
	if v < MinBytesPerSecond {
		return MinBytesPerSecond
	}
	if v > MaxBytesPerSecond {
		return MaxBytesPerSecond
	}
	return uint64(v)
}

// trimmedPercentile discards the single fastest and slowest observations before
// taking the percentile: the fastest is usually a short transfer that never left
// the local cache, and the slowest is usually a stall unrelated to capacity.
// Trimming needs enough samples to still leave a distribution behind, hence the
// length guard.
func trimmedPercentile(samples []Sample, percentile int) float64 {
	rates := make([]float64, 0, len(samples))
	for _, s := range samples {
		if r, err := s.BytesPerSecond(); err == nil {
			rates = append(rates, r)
		}
	}
	if len(rates) == 0 {
		// Observe validates on the way in, so this is unreachable in practice;
		// returning zero makes the caller fall back rather than inventing a rate.
		return 0
	}
	sort.Float64s(rates)
	if len(rates) >= 5 {
		rates = rates[1 : len(rates)-1]
	}
	// Nearest-rank on the low side: for small windows this must not round up
	// into a more optimistic sample than the data supports.
	idx := (percentile * len(rates)) / 100
	if idx >= len(rates) {
		idx = len(rates) - 1
	}
	return rates[idx]
}
