package contribution

import (
	"errors"
	"regexp"
	"strconv"
)

// Parsers for macOS command output. They live here, separate from the code that
// executes anything, for one reason: the sandbox this was written in is Linux,
// so the only way to test the logic honestly is to parse real captured output as
// fixtures. Every parser below is a pure function over bytes and is exercised
// against strings captured from the founder's own Macs in parse_test.go.
//
// Style follows agent.ParseIdle / agent.ParseFreeMemory exactly: a bounded input
// size, an anchored regexp rather than a loose substring search, "exactly one
// match or fail", and an error — never a plausible-looking default — when the
// output does not match. An unparseable reading must surface as Unknown at the
// caller, and Unknown never pauses a host.

// maxOutput mirrors the 256 KiB cap on agent.diagnostic's bounded buffer. A
// parser that will be handed process output should say what it refuses to read.
const maxOutput = 256 << 10

// "Now drawing from 'AC Power'" is the authoritative line. Captured on the
// founder's Apple silicon laptop; the same line is the ONLY line on a desktop.
var drawingRE = regexp.MustCompile(`(?m)^Now drawing from '([A-Za-z0-9 ]{1,32})'\s*$`)

// The battery line, present only on a machine that has one. Captured verbatim:
//
//	-InternalBattery-0 (id=24576099) 100%; charged; 0:00 remaining present: true
//
// The percentage and the state word are optional extras, so the regexp requires
// only the identifier and the percentage; a laptop whose detail text changes in
// a future macOS still parses as "battery present" rather than as a desktop.
var batteryRE = regexp.MustCompile(`(?m)^\s*-\S{1,64}\s+\(id=[0-9]{1,20}\)\s+([0-9]{1,3})%;\s*([a-zA-Z ]{0,32}?)\s*[;\n]`)

// PowerFromBatt parses `/usr/bin/pmset -g batt`.
//
// Three shapes have to be handled, and the third is the one that is easy to get
// wrong:
//
//  1. Laptop on AC:      "AC Power" + an -InternalBattery-0 line.
//  2. Laptop on battery: "Battery Power" + an -InternalBattery-0 line.
//  3. DESKTOP (M4 mini): "AC Power" and no battery line at all.
//
// Shape 3 is a fully KNOWN state, not an unknown and not an error. A desktop
// cannot be on battery, so treating a missing battery line as "cannot tell"
// would make every Mac mini permanently report an unreadable power source, and
// treating it as an error would be worse. Only an unrecognised or absent
// "Now drawing from" line yields Unknown.
func PowerFromBatt(b []byte) (state PowerState, percent int, present bool, detail string, err error) {
	if len(b) > maxOutput {
		return PowerUnknown, -1, false, "", errors.New("power telemetry too large")
	}
	drawing := drawingRE.FindAllSubmatch(b, -1)
	if len(drawing) != 1 {
		// Ambiguous output is refused rather than resolved by picking a match:
		// two "Now drawing from" lines mean we do not understand this output.
		return PowerUnknown, -1, false, "", errors.New("missing or ambiguous power source line")
	}
	switch string(drawing[0][1]) {
	case "AC Power":
		state = PowerAC
	case "Battery Power":
		state = PowerBattery
	default:
		// A source word we have never seen (a future "UPS Power" say) is
		// Unknown on purpose. Mapping the unrecognised case onto AC would be a
		// guess in the exact direction §36.4 protects against.
		return PowerUnknown, -1, false, "", errors.New("unrecognized power source")
	}
	percent = -1
	if m := batteryRE.FindSubmatch(b); m != nil {
		present = true
		detail = string(m[2])
		if p, perr := strconv.Atoi(string(m[1])); perr == nil && p >= 0 && p <= 100 {
			percent = p
		}
		// An out-of-range percentage leaves percent at -1 rather than failing:
		// the charge level is decoration, and the power source — the thing that
		// actually decides — parsed fine.
	}
	return state, percent, present, detail, nil
}

// CPU_Speed_Limit is the one field in `pmset -g therm` that states a throttle
// numerically. Indentation and the tab before "=" vary, so the anchor is the
// field name, not the layout.
var speedLimitRE = regexp.MustCompile(`(?m)^\s*CPU_Speed_Limit\s*=\s*([0-9]{1,3})\s*$`)

// An explicit warning level is the other positive signal. macOS prints
// "Thermal warning level: N" / "Performance warning level: N" when a level HAS
// been recorded; the founder's cool machine printed the "No ... has been
// recorded" notes instead, which is the ambiguous case.
var warningLevelRE = regexp.MustCompile(`(?mi)^\s*(?:thermal|performance) warning level:?\s*=?\s*([0-9]{1,3})\s*$`)

// ThermalFromTherm parses `/usr/bin/pmset -g therm`. It is deliberately
// POSITIVE-ONLY: it can report ThermalThrottled, and it can report
// ThermalUnknown, and it can NEVER report ThermalNominal.
//
// This is the subtlest decision in the package, so here is the whole argument.
// The founder captured this on an Apple silicon laptop that was cool, idle and
// on AC power:
//
//	Note: No thermal warning level has been recorded
//	Note: No performance warning level has been recorded
//	Note: No CPU power status has been recorded
//
// The machine was healthy, so silence is the CORRECT output for a healthy Mac
// where the probe works. But silence is also exactly what a Mac that never
// publishes thermal data at all produces. Absence of a warning is therefore
// indistinguishable from absence of the feature, and no amount of parsing can
// separate the two. Concluding "not throttled" from silence would hand every
// host a green tick backed by nothing — and it is the wrong error to make in
// both directions at once: it would claim knowledge we do not have, and it would
// mask a genuinely throttled machine on models where the field is simply never
// populated.
//
// So: believe a throttle when one is stated, and say Unknown for everything
// else — the three "No ... has been recorded" notes, empty output, unparseable
// output, and a command that failed. Unknown does not pause (see the package
// doc comment), so the cost of this honesty is that thermal protection is
// inert until the Swift bridge in bridge.go exists. That cost is stated in
// status output and in CORE-STATUS.md rather than hidden behind a tick.
func ThermalFromTherm(b []byte) (state ThermalState, speedLimit int, err error) {
	if len(b) > maxOutput {
		return ThermalUnknown, 0, errors.New("thermal telemetry too large")
	}
	if m := speedLimitRE.FindAllSubmatch(b, -1); len(m) == 1 {
		limit, perr := strconv.Atoi(string(m[0][1]))
		if perr != nil || limit <= 0 || limit > 100 {
			return ThermalUnknown, 0, errors.New("invalid CPU_Speed_Limit")
		}
		if limit < ThermalNominalSpeedLimit {
			return ThermalThrottled, limit, nil
		}
		// CPU_Speed_Limit = 100 is the ambiguous case in miniature: it means
		// "not throttled right now" on a machine that reports, and it is also
		// what a machine that has recorded nothing since boot may print. It
		// yields Unknown for the same reason silence does, with the limit
		// carried through so status can show what was read.
		return ThermalUnknown, limit, nil
	}
	// A nonzero warning level is a positive statement of thermal pressure. Level
	// 0 means "no pressure recorded", which is the ambiguous case again.
	if m := warningLevelRE.FindAllSubmatch(b, -1); len(m) >= 1 {
		for _, match := range m {
			if level, perr := strconv.Atoi(string(match[1])); perr == nil && level > 0 {
				return ThermalThrottled, 0, nil
			}
		}
	}
	return ThermalUnknown, 0, nil
}
