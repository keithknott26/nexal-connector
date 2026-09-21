package contribution

import "nexal/connector/internal/throttle"

// PlatformBridge is the ONE Go-side contract for the macOS signals that only
// Swift can read. It does not exist yet on the Swift side, and that is stated
// rather than implied: no implementation of this interface ships in this
// repository, every surface reports the signals it carries as unknown, and
// unknown withholds nothing.
//
// It is deliberately ONE interface carrying TWO signals, replacing what would
// otherwise be two independent TODOs with the same shape:
//
//   - Metered: NWPathMonitor's isExpensive/isConstrained, the gap recorded in
//     internal/throttle (throttle.MeteredSource, commit d93d976).
//   - Thermal: ProcessInfo.processInfo.thermalState
//     (.nominal/.fair/.serious/.critical), the gap this file's package records.
//
// Both are Network.framework/Foundation properties of the same running macOS
// app, both are published as a stream of state changes rather than polled, and
// both need exactly the same plumbing: a Swift observer in the menu-bar app that
// pushes state into the Go agent over the existing authenticated loopback API.
// Building that plumbing twice would mean two IPC shapes, two staleness stories
// and two sets of "is the bridge alive" bugs. One bridge, two signals.
//
// PlatformBridge embeds throttle.MeteredSource rather than restating it, so a
// bridge implementation can be handed directly to the estimator that already
// consumes metered state. That is the whole reason to consolidate: the existing
// consumer does not have to learn about this interface at all.
type PlatformBridge interface {
	throttle.MeteredSource
	// Thermal reports the authoritative OS thermal verdict. Unlike the pmset
	// parser this MAY return ThermalNominal, because ProcessInfo.thermalState
	// distinguishes "no thermal pressure" from "no information" — it always has
	// a value, and the absence case is "no bridge is installed", which is
	// represented by the bridge being nil rather than by a state.
	//
	// The int return is a CPU speed limit percentage when one is known and 0
	// when it is not; thermalState carries no percentage, so a Swift bridge will
	// return 0 and status will say "thermally throttled" without inventing a
	// number.
	Thermal() (ThermalState, int)
}

// Required mapping for whoever writes the Swift side: how
// ProcessInfo.ThermalState must translate into this package's states. It is a
// comment rather than code because the enum is Swift's, but the mapping is a
// product decision and belongs in the repository that acts on it.
//
//
//	.nominal  -> ThermalNominal   (contribute)
//	.fair     -> ThermalNominal   (contribute; .fair is "fans may be audible",
//	                               not "the OS is limiting CPU", and withholding
//	                               on it would stop a Mac mini under any sustained
//	                               load, which is the normal state for a donor)
//	.serious  -> ThermalThrottled (withhold; Apple's own guidance for .serious is
//	                               to reduce work)
//	.critical -> ThermalThrottled (withhold)
//
// .fair mapping to nominal is the one judgement call here, so it is written down
// where it can be argued with rather than discovered in a diff.
