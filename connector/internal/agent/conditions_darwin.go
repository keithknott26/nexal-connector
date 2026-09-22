//go:build darwin

package agent

import (
	"context"
	"time"

	"nexal/connector/internal/contribution"
)

// macOS implementation of the §36.4 platform probe. It is behind a build tag and
// contains NO policy: it shells out, hands the bytes to the pure parsers in
// internal/contribution, and returns whatever they say — including unknown.
// Every decision this feeds is tested on Linux against captured output from the
// founder's own Macs, because this file cannot be executed or verified in the
// Linux sandbox it was written in.
type macConditions struct {
	dataDir string
	// timeout bounds the whole sample. diagnostic() already caps each command at
	// 2 s; this caps the pair, so a probe cannot outlive the interval that
	// schedules it.
	timeout time.Duration
}

// NewConditionSource returns the platform probe for the volume neXal stores its
// data in (the directory holding config.json). dataDir is measured rather than
// "/" because an owner who moved their neXal data to an external volume cares
// about free space THERE, and the boot volume's free space would be a
// confidently wrong answer.
func NewConditionSource(dataDir string) contribution.Source {
	return &macConditions{dataDir: dataDir, timeout: 5 * time.Second}
}

func (m *macConditions) Signals() contribution.Signals {
	// Start from all-unknown. Every field below is only ever set from a
	// successful parse, so a failure at any step leaves unknown rather than a
	// partially plausible reading.
	s := contribution.Signals{At: time.Now(), BatteryPercent: -1,
		ThermalSource: "pmset (positive-only; see contribution.ThermalFromTherm)"}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	// diagnostic() is the house pattern and is reused rather than reimplemented:
	// 2 s context timeout, WaitDelay, a 256 KiB bounded output buffer and a fixed
	// sanitized environment. Absolute paths only, and never through a shell.
	if out, err := diagnostic(ctx, "/usr/bin/pmset", "-g", "batt"); err == nil {
		if state, percent, present, detail, perr := contribution.PowerFromBatt(out); perr == nil {
			s.Power, s.BatteryPercent, s.BatteryPresent, s.BatteryDetail = state, percent, present, detail
		}
	}
	if out, err := diagnostic(ctx, "/usr/bin/pmset", "-g", "therm"); err == nil {
		// The parser can only ever return throttled or unknown from pmset; it
		// never claims a machine is cool. A failed command is the same as silent
		// output: unknown.
		if state, limit, perr := contribution.ThermalFromTherm(out); perr == nil {
			s.Thermal, s.ThermalSpeedLimit = state, limit
		}
	}
	if free, err := contribution.FreeDiskBytes(m.dataDir); err == nil {
		s.DiskKnown, s.FreeDiskBytes, s.DiskVolume = true, free, m.dataDir
	}
	return s
}
