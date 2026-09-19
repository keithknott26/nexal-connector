package agent

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Telemetry struct {
	Known                bool   `json:"known"`
	Synthetic            bool   `json:"synthetic"`
	OwnerActive          bool   `json:"ownerActive"`
	IdleSeconds          uint64 `json:"idleSeconds"`
	TotalMemoryBytes     uint64 `json:"totalMemoryBytes"`
	AvailableMemoryBytes uint64 `json:"availableMemoryBytes"`
}
type Probe func(context.Context) Telemetry

type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("diagnostic output limit exceeded")
	}
	return b.Buffer.Write(p)
}
func diagnostic(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	// Bound inherited output pipes even if a child retains them after the
	// diagnostic process exits or is killed by its context deadline.
	cmd.WaitDelay = time.Second
	b := &boundedBuffer{max: 256 << 10}
	cmd.Stdout = b
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	if err := cmd.Run(); err != nil {
		return nil, errors.New("telemetry unavailable")
	}
	return b.Bytes(), nil
}

var idleRE = regexp.MustCompile(`(?m)^\s*"HIDIdleTime"\s*=\s*([0-9]{1,20})\s*$`)

// ParseIdle accepts only the exact ioreg numeric property, not a broad match
// against similarly named fields, malformed values, floats or shell text.
func ParseIdle(b []byte) (uint64, error) {
	if len(b) > 256<<10 {
		return 0, errors.New("idle telemetry too large")
	}
	m := idleRE.FindAllSubmatch(b, -1)
	if len(m) != 1 {
		return 0, errors.New("missing or ambiguous idle telemetry")
	}
	ns, err := strconv.ParseUint(string(m[0][1]), 10, 64)
	if err != nil {
		return 0, errors.New("invalid idle telemetry")
	}
	return ns / uint64(time.Second), nil
}

var pageRE = regexp.MustCompile(`page size of ([0-9]{1,8}) bytes`)
var freeRE = regexp.MustCompile(`(?m)^Pages free:\s*([0-9]{1,16})\.\s*$`)
var speculativeRE = regexp.MustCompile(`(?m)^Pages speculative:\s*([0-9]{1,16})\.\s*$`)

func ParseFreeMemory(b []byte) (uint64, error) {
	if len(b) > 256<<10 {
		return 0, errors.New("memory telemetry too large")
	}
	pages, frees, specs := pageRE.FindAllSubmatch(b, -1), freeRE.FindAllSubmatch(b, -1), speculativeRE.FindAllSubmatch(b, -1)
	if len(pages) != 1 || len(frees) != 1 || len(specs) != 1 {
		return 0, errors.New("memory telemetry unavailable")
	}
	p, f, s := pages[0], frees[0], specs[0]
	page, e1 := strconv.ParseUint(string(p[1]), 10, 64)
	free, e2 := strconv.ParseUint(string(f[1]), 10, 64)
	spec, e3 := strconv.ParseUint(string(s[1]), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || (page != 4096 && page != 16384) ||
		free > (1<<40)/page || spec > (1<<40)/page || free+spec > (1<<40)/page {
		return 0, errors.New("invalid memory telemetry")
	}
	// Deliberately do not count compressed, active, purgeable or swap memory.
	return (free + spec) * page, nil
}
func MacProbe(idleThreshold uint64) Probe {
	return func(ctx context.Context) Telemetry {
		t := Telemetry{OwnerActive: true}
		if runtime.GOOS != "darwin" {
			return t
		}
		idle, e1 := diagnostic(ctx, "/usr/sbin/ioreg", "-r", "-c", "IOHIDSystem", "-d", "1")
		vm, e2 := diagnostic(ctx, "/usr/bin/vm_stat")
		total, e3 := diagnostic(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
		if e1 != nil || e2 != nil || e3 != nil {
			return t
		}
		idleSeconds, e1 := ParseIdle(idle)
		available, e2 := ParseFreeMemory(vm)
		mem, e3 := strconv.ParseUint(strings.TrimSpace(string(total)), 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || mem < 256<<20 || mem > 1<<40 || available > mem {
			return t
		}
		return Telemetry{Known: true, OwnerActive: idleSeconds < idleThreshold, IdleSeconds: idleSeconds, TotalMemoryBytes: mem, AvailableMemoryBytes: available}
	}
}
