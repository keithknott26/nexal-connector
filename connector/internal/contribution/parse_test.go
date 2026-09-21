package contribution

import "testing"

// Fixtures. Provenance is recorded per fixture, because "captured from the
// founder's Mac" and "written by me to match the documented shape" are very
// different levels of evidence and a test that blurs them is how an untested
// assumption gets shipped as a verified one.
const (
	// CAPTURED, verbatim: founder's Apple silicon laptop (M2), on AC, cool, idle.
	battLaptopACCaptured = "Now drawing from 'AC Power'\n" +
		" -InternalBattery-0 (id=24576099) 100%; charged; 0:00 remaining present: true\n"
	// CONSTRUCTED from the captured shape by changing only the fields that change
	// when a laptop is unplugged. Not captured — pmset was not run on battery, and
	// the founder should confirm this exact line (see CORE-STATUS.md).
	battLaptopDischarging = "Now drawing from 'Battery Power'\n" +
		" -InternalBattery-0 (id=24576099) 61%; discharging; 3:42 remaining present: true\n"
	// EXPECTED shape for the founder's M4 Mac mini: a desktop has no internal
	// battery, so pmset prints the power-source line and nothing else. This is the
	// case that must NOT read as unknown.
	battDesktop = "Now drawing from 'AC Power'\n"
	// CAPTURED, verbatim: `pmset -g therm` on the same cool, idle, AC laptop.
	thermSilentCaptured = "Note: No thermal warning level has been recorded\n" +
		"Note: No performance warning level has been recorded\n" +
		"Note: No CPU power status has been recorded\n"
	// DOCUMENTED Intel-era shape, the only shape that states a throttle
	// numerically. Retained because a parser that only handles the hardware we
	// have is a parser that fails on the hardware a customer has.
	thermThrottled = "2026-09-21 11:00:04 -0400 CPU Power notify\n" +
		"\tCPU_Scheduler_Limit \t= 100\n" +
		"\tCPU_Available_CPUs \t= 8\n" +
		"\tCPU_Speed_Limit \t= 62\n"
	thermFullSpeed = "2026-09-21 11:00:04 -0400 CPU Power notify\n" +
		"\tCPU_Scheduler_Limit \t= 100\n" +
		"\tCPU_Available_CPUs \t= 8\n" +
		"\tCPU_Speed_Limit \t= 100\n"
)

func TestPowerFromBattAcrossMachineShapes(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      string
		state   PowerState
		percent int
		present bool
		detail  string
	}{
		{"captured laptop on AC", battLaptopACCaptured, PowerAC, 100, true, "charged"},
		{"laptop discharging", battLaptopDischarging, PowerBattery, 61, true, "discharging"},
		// The whole point of this row: a desktop is KNOWN to be on AC. Reading it
		// as unknown would make every Mac mini permanently unreadable, and reading
		// it as battery would stop every Mac mini outright.
		{"M4 mini desktop, no battery line", battDesktop, PowerAC, -1, false, ""},
	} {
		state, percent, present, detail, err := PowerFromBatt([]byte(c.in))
		if err != nil || state != c.state || percent != c.percent || present != c.present || detail != c.detail {
			t.Errorf("%s: got %v %d %v %q err=%v", c.name, state, percent, present, detail, err)
		}
	}
}

func TestPowerFromBattRefusesRatherThanGuesses(t *testing.T) {
	for _, bad := range []string{
		"", "pmset: command not found\n",
		// An unrecognised source is Unknown, never mapped onto AC.
		"Now drawing from 'UPS Power'\n",
		// Ambiguous output is refused rather than resolved by picking one.
		battDesktop + battLaptopACCaptured,
		// Shell-looking text must not be treated as a source name.
		"Now drawing from '$(rm -rf /)'\n",
	} {
		state, percent, present, _, err := PowerFromBatt([]byte(bad))
		if err == nil || state != PowerUnknown || percent != -1 || present {
			t.Errorf("guessed a power source from %q: %v %d %v", bad, state, percent, present)
		}
	}
	if _, _, _, _, err := PowerFromBatt(make([]byte, (256<<10)+1)); err == nil {
		t.Fatal("oversized power output accepted")
	}
}

// TestThermalIsPositiveOnly is the single most important test in this package.
// Every input that is not an explicit statement of thermal pressure must yield
// Unknown — never ThermalNominal. The captured all-clear notes came from a
// machine that was genuinely cool, so an all-clear reading would have looked
// correct on that Mac while being unfalsifiable on every other one.
func TestThermalIsPositiveOnly(t *testing.T) {
	for _, c := range []struct {
		name  string
		in    string
		state ThermalState
		limit int
	}{
		{"captured silence on Apple silicon", thermSilentCaptured, ThermalUnknown, 0},
		{"empty output", "", ThermalUnknown, 0},
		{"command noise", "pmset: unrecognized option\n", ThermalUnknown, 0},
		{"speed limit 100 is still not proof of health", thermFullSpeed, ThermalUnknown, 100},
		{"explicit throttle is believed", thermThrottled, ThermalThrottled, 62},
		{"explicit warning level is believed", "Thermal warning level: 2\n", ThermalThrottled, 0},
		{"warning level zero is not a throttle", "Thermal warning level: 0\n", ThermalUnknown, 0},
	} {
		state, limit, err := ThermalFromTherm([]byte(c.in))
		if err != nil || state != c.state || limit != c.limit {
			t.Errorf("%s: got %v %d err=%v", c.name, state, limit, err)
		}
		if state == ThermalNominal {
			t.Errorf("%s: pmset must never report nominal", c.name)
		}
	}
}

func TestThermalRejectsImplausibleLimits(t *testing.T) {
	for _, bad := range []string{"\tCPU_Speed_Limit \t= 0\n", "\tCPU_Speed_Limit \t= 101\n"} {
		if state, _, err := ThermalFromTherm([]byte(bad)); err == nil || state != ThermalUnknown {
			t.Errorf("implausible limit accepted: %q -> %v", bad, state)
		}
	}
	// Two speed-limit lines are ambiguous, so neither is trusted; the warning-level
	// fallback then finds nothing, which is Unknown rather than an error.
	if state, _, err := ThermalFromTherm([]byte(thermThrottled + thermFullSpeed)); err != nil || state != ThermalUnknown {
		t.Fatalf("ambiguous thermal output: %v %v", state, err)
	}
	if _, _, err := ThermalFromTherm(make([]byte, (256<<10)+1)); err == nil {
		t.Fatal("oversized thermal output accepted")
	}
}
