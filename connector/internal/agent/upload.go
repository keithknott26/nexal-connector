package agent

import (
	"errors"

	"nexal/connector/internal/config"
	"nexal/connector/internal/throttle"
)

// This file joins persisted consent (config.ResourcePolicy) to the live signals
// that consent cannot know about: whether the path is metered, and what the
// estimator has measured.
//
// NOTHING HERE THROTTLES AN ACTUAL UPLOAD TODAY. There is no bulk upload path in
// the connector: internal/bundletransfer moves 6 KB enrollment bundles, and the
// Time Machine / JuiceFS storage path that would run for hours is gated behind
// HARDENING-PLAN §21 Step 0 (an untested 200 GB backup and timed restore). The
// limiter, the policy and the measurement are built and tested so that path can
// adopt a reviewed shaper instead of shipping unshaped; until then Status reports
// enforced=false, and no report or UI may say otherwise.

// uploadThrottleLocked resolves mode, measurement and metered state into the one
// number a shaper would use, plus the reason for it. §36.4 requires that a user
// can always see why their Mac is or is not contributing, and §26 puts that in
// the connector UI; a bandwidth ceiling with no visible cause is the same defect
// in a different dimension.
func (a *Agent) uploadThrottleLocked() UploadThrottle {
	policy := a.cfg.ResourcePolicy()
	limit, source := policy.EffectiveUploadLimit()
	out := UploadThrottle{Mode: policy.UploadMode, EffectiveBytesPerSecond: limit,
		Source: source, MeteredStatus: throttle.MeteredUnknown.String(),
		// Honest by construction: no caller uploads bulk data through the
		// limiter yet, so nothing is enforced whatever this number says.
		Enforced: false,
		Reason:   "throttle mechanism ready; no bulk upload path exists yet (HARDENING-PLAN §21 Step 0), so nothing is shaped today"}
	metered := throttle.MeteredUnknown
	if a.metered != nil {
		metered = a.metered.Metered()
	}
	out.MeteredStatus = metered.String()
	switch metered {
	case throttle.MeteredYes:
		// Founder decision: throttle hard but KEEP GOING. A hotspot may be the
		// owner's only link, so a pause is worse for them than a crawl. The
		// ceiling also overrides explicit unlimited mode, because "unlimited"
		// was chosen for a link the owner was not paying for by the gigabyte.
		if limit == 0 || limit > throttle.MeteredCeilingBytesPerSecond {
			out.EffectiveBytesPerSecond = throttle.MeteredCeilingBytesPerSecond
			out.Source = "metered ceiling (transfers continue slowly, never paused)"
		}
		// A rate ceiling is not a volume budget: §16 requires a per-donor
		// monthly bandwidth budget with cap-aware scheduling, and that does not
		// exist. Say so where an owner on a capped plan will read it.
		out.Reason += "; metered path capped by rate, NOT by a monthly volume budget (§16 budget unimplemented)"
	case throttle.MeteredUnknown:
		out.Reason += "; metered status unknown (macOS NWPathMonitor bridge not implemented), so no metered ceiling is applied"
	}
	return out
}

// ApplyMeasuredUploadLimit persists what auto mode measured. It is the seam the
// estimator writes through, kept separate from SetResourcePolicy because a
// measurement is an observation, not owner consent: it must not cancel running
// work, invalidate telemetry or re-fence admission the way a consent change does.
//
// Refused outside auto mode: a manual or unlimited choice is the owner's, and
// quietly storing a measurement under it would make the stored policy lie.
// Refused for out-of-range values so a broken measurement cannot write a
// configuration that Validate would reject on the next load.
func (a *Agent) ApplyMeasuredUploadLimit(bytesPerSecond uint64) error {
	if bytesPerSecond < throttle.MinBytesPerSecond || bytesPerSecond > throttle.MaxBytesPerSecond {
		return errors.New("measured upload rate is outside safe bounds")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if config.NormalizeUploadMode(a.cfg.UploadMode) != config.UploadModeAuto {
		return errors.New("measured upload rate applies only in auto mode")
	}
	if a.cfg.MeasuredUploadBytesPerSecond == bytesPerSecond {
		return nil
	}
	next := a.cfg
	next.MeasuredUploadBytesPerSecond = bytesPerSecond
	if err := config.Save(a.path, next); err != nil {
		// Unlike consent, a failed measurement write is not a safety event: the
		// previous limit — or the conservative default — stays in force, which
		// is the strict direction. Do not pause the host over it.
		return err
	}
	a.cfg = next
	return nil
}
