package agent

import (
	"context"
	"time"

	"nexal/connector/internal/contribution"
)

// Conditional contribution, HARDENING-PLAN §36.4. This file is the seam between
// the platform probes (conditions_darwin.go / conditions_other.go), the pure
// policy in internal/contribution, and the two places where withholding has to
// actually take effect.
//
// THE DESIGN RULE THAT MATTERS MOST HERE: automatic withholding is a SEPARATE,
// NON-PERSISTED state that composes with the owner's pause. It is computed on
// demand from the latest observation and is never written to config.Paused or
// anywhere else on disk.
//
// Writing it into config.Paused would break two things at once and both are
// silent failures:
//
//  1. The owner's own pause would be CLEARED the moment the machine cooled down
//     or was plugged back in, because the automatic path would "resume" a host
//     the owner had deliberately stopped.
//  2. A deliberate pause and a thermal pause would look identical in the config
//     file, in the UI and in support, so nobody could tell whether a host was
//     off because its owner said so or because a probe fired once.
//
// So SetPaused stays the only writer of config.Paused, this state lives in
// memory, and Status reports them as two different sentences.

// ContributionStatus is the §26 "a user can always see why" view of §36.4: every
// condition, its observed value, whether it is withholding, and the plain-language
// reason — including for conditions that are NOT withholding, because "unknown,
// so not pausing" is the state an owner most needs explained.
type ContributionStatus struct {
	contribution.Decision
	// Enforced is TRUE, unlike UploadThrottle.Enforced, and the difference is
	// worth stating precisely rather than copying either claim. Automatic
	// withholding is wired into admitLocked — the same gate that already
	// enforces owner activity and memory headroom — and into the host heartbeat,
	// so a withholding host both refuses local admission and stops being offered
	// work by the coordinator. What is NOT gated is bulk storage/upload traffic,
	// because no such path exists in the connector (see EnforcedScope).
	Enforced      bool   `json:"enforced"`
	EnforcedScope string `json:"enforcedScope"`
	// ObservedAt is when the platform probe last succeeded, empty if never. An
	// owner reading "unknown" deserves to know whether the probe ran at all.
	ObservedAt string `json:"observedAt,omitempty"`
	// ThermalSource says which mechanism produced the thermal verdict. See
	// contribution.PlatformBridge: the authoritative macOS thermal API is Swift
	// and is not bridged yet, so this normally reads "pmset (positive-only)".
	ThermalSource string `json:"thermalSource,omitempty"`
	// Synthetic repeats Telemetry.Synthetic's promise for these dimensions: a
	// development fixture is never presented as a reading from real hardware.
	Synthetic bool `json:"synthetic"`
}

const enforcedScope = "gates local job admission and the host heartbeat's accept flag; " +
	"there is no bulk storage/upload path in the connector to gate (HARDENING-PLAN §21 Step 0)"

// WithConditionSource installs the §36.4 platform probe. Without it every
// condition reads unknown and nothing withholds, which is the same fail-visible
// posture throttle.MeteredSource takes: an absent signal must not be a guess.
func WithConditionSource(s contribution.Source) Option {
	return func(a *Agent) { a.conditionSource = s }
}

// WithPlatformBridge installs the single Swift→Go bridge that carries BOTH the
// metered path signal and the authoritative thermal state
// (contribution.PlatformBridge). No implementation exists yet; when one does,
// this one option replaces WithMeteredSource and upgrades thermal from pmset's
// positive-only reading to ProcessInfo.thermalState, because the bridge is also
// a throttle.MeteredSource.
func WithPlatformBridge(b contribution.PlatformBridge) Option {
	return func(a *Agent) {
		if b != nil {
			a.bridge = b
			a.metered = b
		}
	}
}

// syntheticSource is the development fixture behind `run --dev-assume-idle`,
// matching the existing precedent that the same flag substitutes known synthetic
// idle/memory telemetry and that status marks it as synthetic. It exists because
// a Linux end-to-end test otherwise depends on the free space of whatever machine
// CI happens to run on — which is a real signal, and a real signal that stops the
// host is exactly right in production and useless in a fixture.
//
// It claims the minimum needed: AC power and ample disk. Thermal stays UNKNOWN,
// because there is nothing to gain from faking a dimension that withholds nothing
// when unknown, and a fake "cool" reading is the one claim this whole feature
// refuses to make.
type syntheticSource struct{}

func (syntheticSource) Signals() contribution.Signals {
	return contribution.Signals{At: time.Now(), Synthetic: true,
		Power: contribution.PowerAC, BatteryPercent: -1,
		Thermal: contribution.ThermalUnknown, ThermalSource: "development synthetic fixture",
		DiskKnown: true, FreeDiskBytes: 4 * contribution.MinFreeDiskBytesDefault,
		DiskVolume: "synthetic"}
}

// WithSyntheticConditions installs that fixture. Development-only by
// construction: cmd/nexal refuses the flag that reaches it for a production
// configuration, the same rule --dev-assume-idle already obeys.
func WithSyntheticConditions() Option {
	return func(a *Agent) { a.conditionSource = syntheticSource{} }
}

// RefreshConditions samples the platform probe. It runs on its own slower
// schedule (contribution.ProbeInterval) rather than with the 2 s telemetry tick:
// power source and thermal pressure are not two-second events, and spawning two
// extra processes every two seconds to check whether we are on battery would
// itself cost measurable battery.
//
// Failure leaves the previous observation in place to age out via
// contribution.StaleAfter rather than clearing it, so one slow pmset call does
// not flip a known "on battery" to unknown and resume contributing on a laptop.
func (a *Agent) RefreshConditions(ctx context.Context) {
	a.mu.Lock()
	source, generation := a.conditionSource, a.stateGeneration
	a.mu.Unlock()
	if source == nil {
		return
	}
	s := source.Signals()
	if s.At.IsZero() {
		// A source that reports no timestamp would be treated as permanently
		// stale; stamping it here keeps a test double honest without letting it
		// claim a time it did not observe at.
		s.At = time.Now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil || generation != a.stateGeneration {
		// Same fencing as Refresh: an observation started under a policy the
		// owner has since changed must not decide anything.
		return
	}
	wasWithholding := a.contributionLocked().Withholding
	a.conditions = s
	if a.contributionLocked().Withholding {
		// Cancel running work for the same reason Refresh does: a machine that
		// just went on battery or got hot must stop now, not at the next lease
		// boundary. This cancels the ATTEMPT, not the owner's consent.
		if a.cancel != nil {
			a.cancel()
		}
	}
	// Only a change in withholding needs the coordinator told early. Waking on every
	// probe (every 15 s) doubled the heartbeat rate, since the probe and the heartbeat
	// ticker run on separate 15 s schedules.
	if a.contributionLocked().Withholding != wasWithholding {
		a.wakeHeartbeatLocked()
	}
}

// contributionLocked composes the latest platform observation with the owner
// activity dimension that already exists in Telemetry, and with the Swift bridge
// if one is installed. It is pure apart from reading a.mu-guarded state, so the
// verdict in Status is by construction the verdict admission used.
func (a *Agent) contributionLocked() contribution.Decision {
	s := a.conditions
	// Owner activity is NOT re-measured here. Telemetry already derives it from
	// ioreg HIDIdleTime against the owner-set threshold; this only forwards that
	// answer, with the freshness rule admission uses (10 s), so §36.4's four
	// conditions can be reported in one place without a second implementation.
	fresh := a.telemetry.Known && !a.telemetryAt.IsZero() && time.Since(a.telemetryAt) <= 10*time.Second
	s.OwnerActiveKnown = fresh
	s.OwnerActive = a.telemetry.OwnerActive
	s.IdleSeconds = a.telemetry.IdleSeconds
	s.OwnerOverride = a.ownerOverrideLocked()
	if a.bridge != nil {
		// The bridge is authoritative when present: ProcessInfo.thermalState can
		// distinguish "no thermal pressure" from "no information", which pmset
		// cannot. It may therefore report nominal, which the parser never does.
		state, limit := a.bridge.Thermal()
		s.Thermal, s.ThermalSpeedLimit, s.ThermalSource = state, limit, "ProcessInfo.thermalState bridge"
	}
	return contribution.Evaluate(s, contribution.Policy{
		MinFreeDiskBytes: a.cfg.MinFreeDiskBytes,
	}, time.Now(), a.cfg.Paused)
}

func (a *Agent) contributionStatusLocked() ContributionStatus {
	out := ContributionStatus{Decision: a.contributionLocked(), Enforced: true,
		EnforcedScope: enforcedScope, ThermalSource: a.conditions.ThermalSource,
		Synthetic: a.conditions.Synthetic}
	if !a.conditions.At.IsZero() {
		out.ObservedAt = a.conditions.At.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if a.bridge != nil {
		out.ThermalSource = "ProcessInfo.thermalState bridge"
	}
	if a.conditionSource == nil {
		// Honesty about the shape of the deployment rather than four identical
		// "unknown" lines with no explanation.
		out.EnforcedScope = "no platform condition probe is installed in this process, so every condition reads unknown and nothing is withheld; " + enforcedScope
	}
	return out
}

// Contribution exposes the verdict for callers that want it without the whole
// Status (the local API and the Swift UI read it through Status).
func (a *Agent) Contribution() ContributionStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.contributionStatusLocked()
}
