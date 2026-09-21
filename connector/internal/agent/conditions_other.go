//go:build !darwin

package agent

import (
	"time"

	"nexal/connector/internal/contribution"
)

// Non-Darwin fallback for the §36.4 platform probe. Power source and thermal
// pressure report UNKNOWN — not a fabricated "on AC, cool", which would be a lie
// that reads as a green tick, and not "on battery, hot", which would stop every
// host on a platform we simply cannot measure. Unknown never withholds.
//
// Free disk IS measured here, because syscall.Statfs is real on Linux (see
// contribution/disk_unix.go). That asymmetry is deliberate: a signal we can
// genuinely read should be read, and only the Mac-only signals degrade. It also
// means the disk dimension is exercised against a real filesystem in CI rather
// than against a stub.
type portableConditions struct{ dataDir string }

// NewConditionSource keeps the same constructor name and signature as the Darwin
// build so no caller needs a build tag or a runtime.GOOS branch.
func NewConditionSource(dataDir string) contribution.Source {
	return &portableConditions{dataDir: dataDir}
}

func (p *portableConditions) Signals() contribution.Signals {
	s := contribution.Signals{At: time.Now(), BatteryPercent: -1,
		Power: contribution.PowerUnknown, Thermal: contribution.ThermalUnknown,
		ThermalSource: "unavailable off macOS"}
	if free, err := contribution.FreeDiskBytes(p.dataDir); err == nil {
		s.DiskKnown, s.FreeDiskBytes, s.DiskVolume = true, free, p.dataDir
	}
	return s
}
