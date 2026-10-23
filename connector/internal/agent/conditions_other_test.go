//go:build !darwin

package agent

import (
	"testing"

	"nexal/connector/internal/contribution"
)

// The portable probe must report the Mac-only signals as unknown (never a made-up
// reading) while still measuring free disk for real.
func TestPortableConditionsSignals(t *testing.T) {
	dir := t.TempDir()
	s := NewConditionSource(dir).Signals()

	if s.Power != contribution.PowerUnknown {
		t.Errorf("Power = %v, want unknown", s.Power)
	}
	if s.Thermal != contribution.ThermalUnknown {
		t.Errorf("Thermal = %v, want unknown", s.Thermal)
	}
	if s.BatteryPercent != -1 {
		t.Errorf("BatteryPercent = %d, want -1", s.BatteryPercent)
	}
	if s.ThermalSource == "" {
		t.Error("ThermalSource should explain why thermal is unavailable")
	}
	if !s.DiskKnown || s.DiskVolume != dir {
		t.Errorf("disk not measured for %q: known=%v volume=%q", dir, s.DiskKnown, s.DiskVolume)
	}
	if s.At.IsZero() {
		t.Error("At should be stamped")
	}
}
