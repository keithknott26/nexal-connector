package contribution

import (
	"os"
	"strings"
	"testing"
	"time"
)

// fresh builds an observation that is not stale, with everything unknown unless a
// test says otherwise. Written as a helper so each table row below states only
// the dimension it is about.
func fresh() Signals {
	return Signals{At: time.Now(), BatteryPercent: -1}
}

func find(t *testing.T, d Decision, name string) Condition {
	t.Helper()
	for _, c := range d.Conditions {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("decision reported no %q condition", name)
	return Condition{}
}

// TestUnknownNeverWithholds is the safety property the whole package is built
// around: a machine whose signals cannot be read must keep contributing, because
// an unreadable probe that paused would silently switch off every host in the
// fleet and the fleet would look idle rather than broken.
func TestUnknownNeverWithholds(t *testing.T) {
	d := Evaluate(fresh(), Policy{}, time.Now(), false)
	if d.Withholding {
		t.Fatalf("all-unknown withheld contribution: %s", d.Summary)
	}
	if len(d.Conditions) != 4 {
		t.Fatalf("§36.4 has four conditions, got %d", len(d.Conditions))
	}
	for _, c := range d.Conditions {
		if c.Known || c.Withholding {
			t.Errorf("%s claimed knowledge it does not have", c.Name)
		}
		if c.Reason == "" || c.Value != "unknown" {
			t.Errorf("%s gave no visible reason for being unknown", c.Name)
		}
	}
	// A never-observed host (zero timestamp) is the same case, not a special one.
	if Evaluate(Signals{BatteryPercent: -1}, Policy{}, time.Now(), false).Withholding {
		t.Fatal("a host that has never been probed withheld contribution")
	}
}

// TestStaleObservationsBecomeUnknown asserts the expiry direction: old readings
// are not trusted, and the result is unknown (which does not pause) rather than a
// stale "on battery" that would pause forever after one bad sample.
func TestStaleObservationsBecomeUnknown(t *testing.T) {
	s := fresh()
	s.Power, s.At = PowerBattery, time.Now().Add(-StaleAfter-time.Second)
	d := Evaluate(s, Policy{}, time.Now(), false)
	if d.Withholding || find(t, d, "power").Known {
		t.Fatalf("a stale battery reading still decided: %s", d.Summary)
	}
}

func TestEachConditionWithholdsAndExplainsItself(t *testing.T) {
	for _, c := range []struct {
		name   string
		signal func(*Signals)
		policy Policy
		expect string
	}{
		{"power", func(s *Signals) { s.Power, s.BatteryPercent, s.BatteryPresent = PowerBattery, 61, true }, Policy{}, "battery"},
		{"thermal", func(s *Signals) { s.Thermal, s.ThermalSpeedLimit = ThermalThrottled, 62 }, Policy{}, "heat"},
		{"owner activity", func(s *Signals) { s.OwnerActiveKnown, s.OwnerActive, s.IdleSeconds = true, true, 3 }, Policy{}, "owner"},
		{"free disk", func(s *Signals) { s.DiskKnown, s.FreeDiskBytes = true, 1<<30 }, Policy{}, "reserve"},
	} {
		s := fresh()
		c.signal(&s)
		d := Evaluate(s, c.policy, time.Now(), false)
		got := find(t, d, c.name)
		if !d.Withholding || !got.Withholding || !got.Known {
			t.Errorf("%s did not withhold: %+v", c.name, got)
		}
		if !strings.Contains(got.Reason, c.expect) {
			t.Errorf("%s reason does not explain itself: %q", c.name, got.Reason)
		}
		if !strings.Contains(d.Summary, c.name) {
			t.Errorf("summary omits the withholding condition: %q", d.Summary)
		}
		// The §26 requirement in one assertion: an owner reading status must see
		// the observed value, not just a verdict.
		if got.Value == "" || got.Value == "unknown" {
			t.Errorf("%s reported no value: %q", c.name, got.Value)
		}
	}
}

// TestDesktopBatteryConditionCanNeverWithhold covers the founder's M4 mini: AC
// power with no battery line at all is a fully known, healthy state.
func TestDesktopBatteryConditionCanNeverWithhold(t *testing.T) {
	s := fresh()
	state, percent, present, _, err := PowerFromBatt([]byte(battDesktop))
	if err != nil {
		t.Fatal(err)
	}
	s.Power, s.BatteryPercent, s.BatteryPresent = state, percent, present
	d := Evaluate(s, Policy{}, time.Now(), false)
	got := find(t, d, "power")
	if d.Withholding || got.Withholding || !got.Known {
		t.Fatalf("desktop power state mishandled: %+v", got)
	}
	if !strings.Contains(got.Value, "desktop") || !strings.Contains(got.Reason, "never withhold") {
		t.Fatalf("desktop state not explained to the owner: %+v", got)
	}
}

// TestOwnerPauseAndAutomaticWithholdingAreSeparateSentences guards the design
// trap at the reporting layer: the two facts must be distinguishable in output,
// not merged into one boolean.
func TestOwnerPauseAndAutomaticWithholdingAreSeparateSentences(t *testing.T) {
	clear := Evaluate(fresh(), Policy{}, time.Now(), true)
	if clear.Withholding {
		t.Fatal("the owner's pause was reported as an automatic condition")
	}
	if !clear.OwnerPaused || !strings.Contains(clear.Summary, "deliberate") {
		t.Fatalf("owner pause not distinguished: %+v", clear)
	}
	hot := fresh()
	hot.Thermal = ThermalThrottled
	both := Evaluate(hot, Policy{}, time.Now(), true)
	if !both.Withholding || !both.OwnerPaused {
		t.Fatalf("a paused AND hot host lost one of the two facts: %+v", both)
	}
}

func TestOwnerOverrideSuppressesOnlyOwnerActivity(t *testing.T) {
	s := fresh()
	s.OwnerActiveKnown, s.OwnerActive, s.OwnerOverride = true, true, true
	if d := Evaluate(s, Policy{}, time.Now(), false); d.Withholding {
		t.Fatalf("explicit owner consent was ignored: %s", d.Summary)
	}
	// The override is about the owner's own attention, and says nothing about
	// heat: a machine the owner allowed to work while active must still stop when
	// it is throttling.
	s.Thermal = ThermalThrottled
	if d := Evaluate(s, Policy{}, time.Now(), false); !d.Withholding {
		t.Fatal("owner override suppressed the thermal condition")
	}
}

func TestDiskFloorIsOwnerTunableAndNeverZero(t *testing.T) {
	if (Policy{}).MinFreeDisk() != MinFreeDiskBytesDefault {
		t.Fatal("an unset disk floor must resolve to the reviewed default, not to zero")
	}
	s := fresh()
	s.DiskKnown, s.FreeDiskBytes = true, 6<<30
	// Below the 10 GiB default: withholds.
	if !Evaluate(s, Policy{}, time.Now(), false).Withholding {
		t.Fatal("6 GiB free did not trip the default floor")
	}
	// An owner with a small SSD may lower it, and that must be honoured.
	if Evaluate(s, Policy{MinFreeDiskBytes: 2 << 30}, time.Now(), false).Withholding {
		t.Fatal("owner-lowered floor was ignored")
	}
	// Exactly at the floor is not below it.
	s.FreeDiskBytes = MinFreeDiskBytesDefault
	if Evaluate(s, Policy{}, time.Now(), false).Withholding {
		t.Fatal("free space exactly at the floor withheld")
	}
}

func TestSyntheticConditionsAreLabelledButStillDecide(t *testing.T) {
	s := fresh()
	s.Synthetic, s.Thermal = true, ThermalThrottled
	d := Evaluate(s, Policy{}, time.Now(), false)
	if !d.Withholding {
		t.Fatal("a synthetic reading must gate work exactly as a real one does")
	}
	if !strings.Contains(d.Summary, "SYNTHETIC") {
		t.Fatalf("a fixture was presented as a real measurement: %q", d.Summary)
	}
}

// TestFreeDiskBytesReadsARealFilesystem exercises the one dimension that is
// genuinely measurable in this Linux sandbox, so the disk path is not a stub.
func TestFreeDiskBytesReadsARealFilesystem(t *testing.T) {
	dir := t.TempDir()
	free, err := FreeDiskBytes(dir)
	if err != nil {
		t.Fatalf("statfs on a real directory failed: %v", err)
	}
	if free == 0 {
		t.Fatal("a writable temporary directory reported zero free bytes")
	}
	// A path that does not exist must be an error, which the caller turns into
	// unknown — not a zero that would read as "disk full" and stop the host.
	if _, err := FreeDiskBytes(dir + "/definitely-absent"); err == nil {
		t.Fatal("statfs on a missing path reported success")
	}
	if err != nil && strings.Contains(err.Error(), dir) {
		t.Fatal("disk error text leaked a filesystem path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("test directory disturbed: %v", err)
	}
}
