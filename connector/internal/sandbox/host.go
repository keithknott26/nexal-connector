package sandbox

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Host reports facts about this Mac. It is an interface so the manager is
// testable and so the agent can plug in its own cached readings.
type Host interface {
	Facts(ctx context.Context, diskPath string) (HostFacts, error)
}

// SystemHost reads facts with fixed system tools at absolute paths (no cgo).
type SystemHost struct{}

// Facts implements Host.
func (SystemHost) Facts(ctx context.Context, diskPath string) (HostFacts, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f := HostFacts{CPUs: runtime.NumCPU()}

	out, err := execRunner(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return f, errors.New("cannot read host memory")
	}
	b, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return f, errors.New("cannot read host memory")
	}
	f.MemoryMB = b >> 20

	out, err = execRunner(ctx, "/bin/df", "-Pk", diskPath)
	if err != nil {
		return f, errors.New("cannot read free disk space")
	}
	if f.FreeDisk, err = ParseDFFree(string(out)); err != nil {
		return f, err
	}

	out, err = execRunner(ctx, "/usr/bin/pmset", "-g", "batt")
	if err != nil {
		f.PowerUnknown = true
	} else {
		f.OnBattery = ParseOnBattery(string(out))
	}
	return f, nil
}

// ParseDFFree extracts the Available column, in bytes, from `df -Pk` output
// (header line, then one data line: fs 1024-blocks used available capacity mount).
// It is pure.
func ParseDFFree(out string) (uint64, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, errors.New("unexpected df output")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, errors.New("unexpected df output")
	}
	kb, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return 0, errors.New("unexpected df output")
	}
	return kb << 10, nil
}

// ParseOnBattery reports whether `pmset -g batt` says the Mac is drawing from
// the battery. It is pure.
func ParseOnBattery(out string) bool {
	return strings.Contains(out, "'Battery Power'")
}
