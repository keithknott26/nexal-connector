// Package contribution decides whether this machine should be donating
// resources right now, and says why in plain language.
//
// HARDENING-PLAN §36.4 is the whole reason this package exists. Its wording is
// load-bearing, so it is quoted rather than paraphrased:
//
//	"And default-on must be conditional, never unconditional: pause when on
//	 battery, when thermally throttled, when the user is actively working, or
//	 when disk is low. These are table stakes for anything that consumes a
//	 machine in the background — and they should be visible in the connector UI
//	 per §26, so a user can always see why their Mac is or is not contributing."
//
// Four conditions, therefore four dimensions here. One of them — "the user is
// actively working" — already exists and is NOT reimplemented: agent.Telemetry
// carries OwnerActive/IdleSeconds derived from ioreg HIDIdleTime against the
// owner-set config.IdleSeconds threshold. This package accepts that verdict as
// an input so all four conditions can be reported and reasoned about in ONE
// place, which is what §26's "always see why" requires.
//
// Two conventions are inherited deliberately from internal/throttle (the
// metered-path work, commit d93d976), because an unavailable platform signal is
// exactly the same problem in a new dimension:
//
//   - Unknown is a FIRST-CLASS state and is never guessed in either direction.
//   - The probe is an injectable interface, so every policy decision below is
//     tested on Linux with captured macOS fixtures.
//
// And the rule that governs every Unknown in this file: UNKNOWN MUST NOT PAUSE.
// A machine whose thermal or power state cannot be read keeps contributing,
// with the reason visible. The alternative is catastrophic in the quiet
// direction: a probe that returns nothing on some Mac model — which is a real
// possibility for pmset -g therm on Apple silicon — would silently switch off
// every host in the fleet, and the fleet would look idle rather than broken.
package contribution

import (
	"fmt"
	"time"
)

// Thresholds. Every number is a product decision, so each one states what it is
// derived from; an unreviewable constant here would decide whether someone's
// laptop runs hot, which is not a thing to invent silently.
const (
	// MinFreeDiskBytesDefault is the disk-low floor when the owner has not set
	// one.
	//
	// It is anchored on the founder's standing free-drive decision (§39.6,
	// "RESOLVED: free drive is 5 GB"): a single free-tier neXal Drive can
	// legitimately place 5 GB of someone else's data on this host, so a floor
	// below 5 GB could be consumed entirely by one account's allowance and
	// leave the owner at zero. 10 GiB is that allowance plus an equal margin
	// for the machine's own needs — macOS itself needs several GB of writable
	// space for swap, snapshots and updates, and a Mac that runs out of disk
	// does not degrade gracefully, it stops.
	//
	// The shape mirrors config.ReserveMemoryBytes, which is the existing
	// precedent for the same idea in a different resource: reserve a slice for
	// the owner and admit work only from what is left over. This is the disk
	// equivalent of that reserve, not a guess at how much neXal will store.
	MinFreeDiskBytesDefault = 10 << 30
	// Owner-tunable bounds (config.ResourcePolicy.MinFreeDiskBytes). The floor
	// is 1 GiB because below that macOS is already in trouble on its own and a
	// "reserve" that small is theatre; the ceiling matches
	// ReserveMemoryBytes's 1 TiB ceiling rather than inventing a second shape.
	MinFreeDiskBytesFloor   = 1 << 30
	MinFreeDiskBytesCeiling = 1 << 40

	// ThermalNominalSpeedLimit is the CPU_Speed_Limit value that means "full
	// speed available". Apple documents no tolerance band, so anything BELOW 100
	// is treated as throttled rather than inventing a threshold like 90: the OS
	// is already shedding CPU, and adding donated work to a machine that is
	// shedding heat is precisely what §36.4 forbids.
	//
	// Reading 100 is NOT evidence of a healthy machine — see ParseThermal, which
	// is positive-only. This constant is only the boundary for believing a
	// throttle report, never for issuing an all-clear.
	ThermalNominalSpeedLimit = 100

	// ProbeInterval bounds how often the platform probes run. Telemetry
	// (memory/idle) is sampled every 2 s because admission depends on it
	// second-by-second; power source and thermal pressure are not 2-second
	// events, and spawning two extra processes every two seconds would itself
	// cost measurable battery — which would be an absurd way to implement a
	// battery protection.
	ProbeInterval = 15 * time.Second
	// StaleAfter downgrades old observations to Unknown rather than trusting
	// them. Six probe intervals is long enough to survive a couple of missed
	// samples and short enough that an unplugged laptop is noticed quickly.
	// Note which way this fails: stale becomes Unknown, and Unknown does not
	// pause. That is the §36.4-safe direction per this package's doc comment —
	// a broken probe must not disable a fleet — and the reason string says so.
	StaleAfter = 6 * ProbeInterval
)

// PowerState is where the machine is drawing power from. Unknown is never
// guessed: guessing "battery" would stop every Mac mini in the fleet, and
// guessing "AC" would drain the laptop §36.4 is written to protect.
type PowerState int

const (
	PowerUnknown PowerState = iota
	PowerAC
	PowerBattery
)

func (p PowerState) String() string {
	switch p {
	case PowerAC:
		return "ac"
	case PowerBattery:
		return "battery"
	default:
		return "unknown"
	}
}

// ThermalState is whether the OS is currently limiting CPU for thermal reasons.
//
// ThermalUnknown is the NORMAL, expected state on the hardware we have evidence
// for, not an exception, and every surface must treat it that way. The founder
// captured `pmset -g therm` on his Apple silicon laptop while it was cool, idle
// and on AC:
//
//	Note: No thermal warning level has been recorded
//	Note: No performance warning level has been recorded
//	Note: No CPU power status has been recorded
//
// That sample is why this dimension is POSITIVE-ONLY. Because the machine was
// genuinely cool, empty output is EXPECTED on a Mac where the probe works — so
// "no warning recorded" is indistinguishable from "this Mac never records
// warnings at all". Silence proves nothing in either direction and can only
// mean Unknown.
//
// ThermalNominal therefore CANNOT be produced by the pmset parser. It exists for
// the authoritative source only: Swift's ProcessInfo.processInfo.thermalState
// (.nominal/.fair/.serious/.critical), which cannot be compiled or run in this
// sandbox — see PlatformBridge in bridge.go. powermetrics(8) is deliberately not
// used: it requires root, and a user-level agent that asks for sudo to check its
// own temperature is a worse product than one that reports "unknown".
type ThermalState int

const (
	ThermalUnknown ThermalState = iota
	ThermalNominal
	ThermalThrottled
)

func (t ThermalState) String() string {
	switch t {
	case ThermalNominal:
		return "nominal"
	case ThermalThrottled:
		return "throttled"
	default:
		return "unknown"
	}
}

// Signals is one observation of the host, as read from the platform. Every
// dimension carries its own known/unknown marker, mirroring
// agent.Telemetry.Known: a struct where zero means both "false" and "not
// measured" is how a fabricated reading gets shipped.
type Signals struct {
	// At is when the observation was taken, so Evaluate can expire it. Zero
	// means "never observed", which is Unknown everywhere.
	At time.Time `json:"at"`
	// Synthetic marks a development fixture, exactly as agent.Telemetry.Synthetic
	// does for idle/memory. It changes no decision — a fake reading must gate work
	// the same way a real one does, or the gate is untested — but it is carried
	// into the summary so no status output, report or UI can present a fixture as
	// a measurement of real hardware.
	Synthetic bool `json:"synthetic"`

	Power PowerState `json:"power"`
	// BatteryPercent is informational only and is NEVER a threshold: §36.4
	// says pause on battery, full stop. A machine at 100% on battery is still
	// draining the owner's remaining runtime for someone else's job. -1 means
	// not reported.
	BatteryPercent int `json:"batteryPercent"`
	// BatteryPresent distinguishes a laptop from a desktop. An M4 Mac mini prints
	// "Now drawing from 'AC Power'" and NOTHING else: that is a fully known,
	// perfectly healthy desktop state, not an unknown and not an error. A desktop
	// can never be on battery, so this dimension never withholds there.
	BatteryPresent bool `json:"batteryPresent"`
	// BatteryDetail is the verbatim-ish extra pmset gives on a laptop
	// ("charged", "discharging", "AC attached"), useful in support and never used
	// as a decision input.
	BatteryDetail string `json:"batteryDetail,omitempty"`

	Thermal           ThermalState `json:"thermal"`
	ThermalSpeedLimit int          `json:"thermalSpeedLimit"` // percent of nominal CPU speed, 0 = unreported
	// ThermalSource names where the verdict came from, because "unknown because
	// this Mac publishes nothing" and "unknown because no Swift bridge is
	// installed" are different support problems.
	ThermalSource string `json:"thermalSource,omitempty"`

	DiskKnown     bool   `json:"diskKnown"`
	FreeDiskBytes uint64 `json:"freeDiskBytes"`
	// DiskVolume is the path that was measured — the directory neXal stores its
	// own data in — so a support conversation can tell an external-volume
	// install from a boot-volume one. It is a local path and never leaves the
	// machine's own status output.
	DiskVolume string `json:"diskVolume,omitempty"`

	// OwnerActive and friends are NOT measured here. They come from
	// agent.Telemetry, which already implements idle detection against the
	// owner-set threshold; duplicating it would create a second answer to the
	// same question.
	OwnerActiveKnown bool   `json:"ownerActiveKnown"`
	OwnerActive      bool   `json:"ownerActive"`
	IdleSeconds      uint64 `json:"idleSeconds"`
	// OwnerOverride is the owner's explicit "accept jobs now" grant. When it is
	// set, owner activity does not withhold, because the owner just said
	// otherwise about their own machine.
	OwnerOverride bool `json:"ownerOverride"`
}

// Source reads the platform. It is an interface for the same two reasons
// throttle.MeteredSource is: the real implementation is macOS-only and cannot
// execute in this sandbox, and every policy decision must be testable offline
// against captured output.
type Source interface {
	Signals() Signals
}

// Policy is the owner-tunable part. Only the disk floor is tunable, and that is
// a deliberate asymmetry: disk headroom genuinely differs between a 256 GB
// MacBook Air and an 8 TB Studio, whereas "do not run on battery" and "do not
// run while thermally throttled" are safety properties of §36.4 rather than
// preferences, and an owner-facing switch to disable them would be a switch to
// re-create the support incident §36.4 exists to prevent.
type Policy struct {
	MinFreeDiskBytes uint64
}

// MinFreeDisk resolves the effective floor. Zero means "owner never chose", not
// "no floor": a zero floor would mean contributing until the volume is full.
func (p Policy) MinFreeDisk() uint64 {
	if p.MinFreeDiskBytes == 0 {
		return MinFreeDiskBytesDefault
	}
	return p.MinFreeDiskBytes
}

// Condition is one §36.4 dimension rendered for a human. Value is what was
// observed, Withholding is whether this dimension alone is stopping
// contribution, and Reason is the plain-language why — including, importantly,
// the why for a condition that is NOT withholding, because "unknown, so not
// pausing" is exactly the state an owner needs to be told about.
type Condition struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Known       bool   `json:"known"`
	Withholding bool   `json:"withholding"`
	Reason      string `json:"reason"`
}

// Decision is the composed verdict.
//
// Withholding here is AUTOMATIC and TRANSIENT and has nothing to do with
// config.Paused. That separation is the entire point and is asserted by tests:
// config.Paused is the owner's deliberate, persisted choice, so writing an
// automatic thermal event into it would (a) make the owner's own pause
// indistinguishable from a machine-made one and (b) silently CLEAR the owner's
// pause when the machine cooled down. Nothing in this package persists
// anything.
type Decision struct {
	// Withholding is true when at least one condition is withholding.
	Withholding bool        `json:"withholding"`
	Conditions  []Condition `json:"conditions"`
	// Summary is one sentence for the menu bar and for `nexal status`.
	Summary string `json:"summary"`
	// OwnerPaused is reported alongside, never merged: the UI must be able to
	// say "you paused this" and "your Mac is hot" as different sentences.
	OwnerPaused bool `json:"ownerPaused"`
}

// Evaluate composes the four conditions into a verdict. It is a pure function
// of an observation and a policy, which is what makes the whole feature
// testable on a Linux sandbox with no Mac in sight.
func Evaluate(s Signals, p Policy, now time.Time, ownerPaused bool) Decision {
	// Expire the observation first. Everything below then reads either a fresh
	// reading or an explicit Unknown; there is no third case where a value is
	// silently minutes old.
	stale := s.At.IsZero() || now.Sub(s.At) > StaleAfter
	d := Decision{OwnerPaused: ownerPaused}
	d.Conditions = append(d.Conditions,
		powerCondition(s, stale),
		thermalCondition(s, stale),
		ownerCondition(s),
		diskCondition(s, p, stale))
	withheld := make([]string, 0, len(d.Conditions))
	for _, c := range d.Conditions {
		if c.Withholding {
			d.Withholding = true
			withheld = append(withheld, c.Name)
		}
	}
	switch {
	case d.Withholding:
		d.Summary = "not contributing right now: " + join(withheld) +
			" (automatic and temporary; this is not the pause switch)"
	case ownerPaused:
		// Report the conditions honestly even when the owner has paused: the
		// owner's pause is the reason nothing is running, and conflating the two
		// would hide a hot machine behind a pause the owner may be about to lift.
		d.Summary = "contribution conditions are all clear; the host is paused by the owner, which is a separate, deliberate choice"
	default:
		d.Summary = "contributing: no §36.4 condition is withholding"
	}
	if s.Synthetic {
		d.Summary = "DEVELOPMENT SYNTHETIC CONDITIONS — " + d.Summary
	}
	return d
}

// join renders a short list without pulling in a formatting dependency for what
// is at most four words.
func join(names []string) string {
	out := ""
	for i, n := range names {
		switch {
		case i == 0:
		case i == len(names)-1:
			out += " and "
		default:
			out += ", "
		}
		out += n
	}
	return out
}

func powerCondition(s Signals, stale bool) Condition {
	c := Condition{Name: "power"}
	if stale || s.Power == PowerUnknown {
		c.Value = "unknown"
		// Unknown does not pause. A Mac mini whose pmset output we failed to
		// parse must keep contributing, or one parser miss switches off every
		// desktop in the fleet.
		c.Reason = "power source could not be read, so it is not withholding; an unreadable probe must never silently stop a host"
		return c
	}
	c.Known = true
	if s.Power == PowerBattery {
		c.Withholding = true
		c.Value = "on battery"
		if s.BatteryPercent >= 0 {
			c.Value = fmt.Sprintf("on battery (%d%%)", s.BatteryPercent)
		}
		// No percentage threshold on purpose: §36.4 says pause on battery, and
		// any charge level still spends the owner's remaining runtime.
		c.Reason = "running on battery: donating CPU/GPU would spend the owner's remaining runtime, so contribution waits for AC power"
		return c
	}
	// Desktop and laptop are both fully known AC states, and the distinction is
	// reported because it changes what the owner should expect to see: a Mac mini
	// will never show this condition withholding, and an owner who never sees a
	// state should be told it cannot happen rather than left wondering.
	if !s.BatteryPresent {
		c.Value = "on AC power (desktop, no internal battery)"
		c.Reason = "a desktop Mac has no battery to drain, so this condition can never withhold on this machine"
		return c
	}
	c.Value = "on AC power"
	if s.BatteryPercent >= 0 {
		c.Value = fmt.Sprintf("on AC power (battery %d%%)", s.BatteryPercent)
	}
	c.Reason = "mains power, so battery life is not at stake"
	return c
}

func thermalCondition(s Signals, stale bool) Condition {
	c := Condition{Name: "thermal"}
	if stale || s.Thermal == ThermalUnknown {
		c.Value = "unknown"
		// The expected state on Apple silicon, and worth spelling out where an
		// owner reads it: pmset's silence is ambiguous, so neither answer is
		// claimed. Unknown never withholds, per the package doc comment.
		c.Reason = "thermal pressure is not reported by this Mac; pmset is silent both when a Mac is cool and when it cannot report at all, so neither 'throttled' nor 'fine' can be claimed — and unknown never withholds"
		return c
	}
	c.Known = true
	if s.Thermal == ThermalThrottled {
		c.Withholding = true
		// A speed limit is present only when pmset actually published one; a
		// bridge-sourced .serious/.critical verdict carries no percentage, and
		// printing "throttled to 0%" would be a fabricated number.
		c.Value = "thermally throttled"
		if s.ThermalSpeedLimit > 0 {
			c.Value = fmt.Sprintf("throttled to %d%% of nominal CPU speed", s.ThermalSpeedLimit)
		}
		c.Reason = "the OS is already limiting CPU for heat, so donated work would add heat and fan noise to a machine that is shedding both"
		return c
	}
	// Only a real thermal API (PlatformBridge / ProcessInfo.thermalState) can
	// reach here; the pmset parser never reports nominal.
	c.Value = "nominal"
	c.Reason = "the OS thermal API reports no thermal pressure"
	return c
}

// ownerCondition reports the dimension agent.Telemetry already measures. It is
// reproduced here for §26 visibility only — the detection, the HIDIdleTime
// parse and the owner-set threshold all stay where they are.
// ownerCondition deliberately ignores the staleness of the platform probe. The
// owner-activity verdict comes from agent.Telemetry, which is sampled every 2 s
// on its own schedule and carries its own freshness rule (admission refuses
// telemetry older than 10 s); OwnerActiveKnown is that rule's answer. Expiring
// it against ProbeInterval instead would discard a fresh reading because an
// unrelated pmset call was slow.
func ownerCondition(s Signals) Condition {
	c := Condition{Name: "owner activity"}
	if !s.OwnerActiveKnown {
		c.Value = "unknown"
		// Note the asymmetry with agent.admitLocked, which DOES refuse work on
		// unknown telemetry. That is correct there: admission requires positive
		// evidence of headroom. Here the question is the narrower "is a §36.4
		// condition withholding", and an unreadable idle timer is not evidence
		// that the owner is typing.
		c.Reason = "idle time could not be read; admission separately requires fresh telemetry, so this dimension does not withhold on its own"
		return c
	}
	c.Known = true
	if s.OwnerActive && !s.OwnerOverride {
		c.Withholding = true
		c.Value = fmt.Sprintf("owner active (idle %ds)", s.IdleSeconds)
		c.Reason = "the owner is using this Mac, and the owner's own work always outranks donated work"
		return c
	}
	if s.OwnerActive {
		c.Value = fmt.Sprintf("owner active (idle %ds), overridden by the owner", s.IdleSeconds)
		c.Reason = "the owner explicitly asked to accept jobs while active"
		return c
	}
	c.Value = fmt.Sprintf("idle %ds", s.IdleSeconds)
	c.Reason = "no owner input for longer than the owner-set idle threshold"
	return c
}

func diskCondition(s Signals, p Policy, stale bool) Condition {
	c := Condition{Name: "free disk"}
	floor := p.MinFreeDisk()
	if stale || !s.DiskKnown {
		c.Value = "unknown"
		c.Reason = "free space on the neXal data volume could not be read, so it is not withholding; a failed statfs must not stop a healthy host"
		return c
	}
	c.Known = true
	c.Value = fmt.Sprintf("%s free of a %s floor", gib(s.FreeDiskBytes), gib(floor))
	if s.DiskVolume != "" {
		c.Value += " on " + s.DiskVolume
	}
	if s.FreeDiskBytes < floor {
		c.Withholding = true
		c.Reason = "free space is below the owner's disk reserve; filling an owner's volume with donated data is the disk equivalent of spending their memory reserve"
		return c
	}
	c.Reason = "free space is above the owner's disk reserve"
	return c
}

// gib renders bytes the way an owner reads them. One decimal place, because
// "10.4 GiB" is useful and "10.4237 GiB" is noise.
func gib(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30))
}
