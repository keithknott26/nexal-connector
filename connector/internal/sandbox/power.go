package sandbox

import (
	"context"
	"strings"
	"time"
)

// Sleep/wake observation without IOKit, cgo or a keep-awake assertion.
//
// Wake: a ticker compares the wall clock with the monotonic clock. On macOS Go's
// monotonic clock does not advance while the Mac sleeps, so the wall delta minus
// the monotonic delta is the time spent asleep (SleepGap). A gap above the
// threshold means "we just woke".
//
// Sleep (best effort): a notification before sleep is only available through
// IOKit/NSWorkspace, so the runner uses a heuristic that covers the common
// laptop case: lid closed (ioreg AppleClamshellState). When the lid closes the
// runner announces awake:false; if the Mac turns out to stay awake (clamshell
// mode with a display) it announces awake:true again after a grace period. A
// sleep that has no lid event (menu, idle) is only noticed on wake; the Mac app
// can improve this by calling the connector's local hook (see README).

// DefaultSleepThreshold is how much clock gap counts as a sleep.
const DefaultSleepThreshold = 15 * time.Second

// SleepGap returns the time spent asleep given the wall-clock and monotonic
// deltas of the same interval, or 0 if below threshold. It is pure.
func SleepGap(wall, mono, threshold time.Duration) time.Duration {
	gap := wall - mono
	if gap < threshold {
		return 0
	}
	return gap
}

// SleepDetector turns successive time.Now() readings into sleep gaps.
type SleepDetector struct {
	Threshold time.Duration
	last      time.Time
}

// Check reports the sleep duration since the previous call (0 if none). now must
// come from time.Now() so it carries a monotonic reading.
func (d *SleepDetector) Check(now time.Time) time.Duration {
	th := d.Threshold
	if th <= 0 {
		th = DefaultSleepThreshold
	}
	prev := d.last
	d.last = now
	if prev.IsZero() {
		return 0
	}
	wall := now.Round(0).Sub(prev.Round(0)) // Round(0) strips the monotonic reading
	mono := now.Sub(prev)                   // uses it when both have one
	return SleepGap(wall, mono, th)
}

// ParseLidClosed reports whether `ioreg -r -k AppleClamshellState -d 4` output
// says the lid is closed. ok is false when the machine has no lid (desktop Mac)
// or the output is unrecognized. It is pure.
func ParseLidClosed(out string) (closed, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "\"AppleClamshellState\"") {
			continue
		}
		switch {
		case strings.HasSuffix(line, "= Yes"):
			return true, true
		case strings.HasSuffix(line, "= No"):
			return false, true
		}
	}
	return false, false
}

// LidReader reports whether the lid is closed.
type LidReader func(ctx context.Context) (closed, ok bool)

// IORegLid is the default LidReader (fixed absolute path, no cgo).
func IORegLid(ctx context.Context) (bool, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := execRunner(ctx, "/usr/sbin/ioreg", "-r", "-k", "AppleClamshellState", "-d", "4")
	if err != nil {
		return false, false
	}
	return ParseLidClosed(string(out))
}

// lidGrace is how long the lid may stay closed with the Mac still awake before
// the runner takes back its awake:false announcement.
const lidGrace = 20 * time.Second

// PowerTracker is the pure state machine behind the lid heuristic.
type PowerTracker struct {
	announced bool      // awake:false is currently announced
	closedAt  time.Time // when the lid closed
	stillShut bool      // lid closed but the Mac stayed awake; announced was taken back
}

// PowerAction is what the caller should report.
type PowerAction int

const (
	PowerNone PowerAction = iota
	PowerAnnounceSleep
	PowerAnnounceAwake
)

// Observe feeds one lid reading and returns the action to take.
func (p *PowerTracker) Observe(closed bool, now time.Time) PowerAction {
	switch {
	case closed && !p.announced && !p.stillShut:
		p.announced, p.closedAt = true, now
		return PowerAnnounceSleep
	case closed && p.announced && now.Sub(p.closedAt) >= lidGrace:
		p.announced, p.stillShut = false, true
		return PowerAnnounceAwake
	case !closed:
		wasAnnounced := p.announced
		p.announced, p.stillShut = false, false
		if wasAnnounced {
			return PowerAnnounceAwake
		}
	}
	return PowerNone
}

// Woke records that the Mac woke (a clock gap was seen): any announcement is over.
func (p *PowerTracker) Woke() {
	p.announced = false
}
