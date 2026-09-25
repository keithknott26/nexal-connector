// Package sysinfo collects the host details shown for each computer in the
// neXal panel: OS, model, chip, cores, memory, disk and thermal state.
//
// Everything is read locally with fixed system tools at absolute paths and the
// standard library; nothing here needs administrator rights or cgo. The static
// part (OS, model, chip, cores, memory, disk size) is read once per process and
// cached; only disk free space and thermal state are re-read on each Collect.
//
// TEMPERATURE. macOS exposes exact sensor temperatures only to root
// (powermetrics) or through private IOKit interfaces that need cgo. What an
// unprivileged process CAN read reliably is the kernel's thermal pressure,
// reported here as Thermal: "nominal", "throttled" (CPU speed limited), or
// "unknown".
package sysinfo

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Info is one host's details. Every field is optional: zero means unknown.
// JSON names are the wire contract with the coordinator's host.info frame.
type Info struct {
	OS               string `json:"os,omitempty"`    // "macOS 27.0 (27A5218g)"
	Model            string `json:"model,omitempty"` // "Mac mini"
	Chip             string `json:"chip,omitempty"`  // "Apple M4"
	Cores            int    `json:"cores,omitempty"`
	PerformanceCores int    `json:"performanceCores,omitempty"`
	EfficiencyCores  int    `json:"efficiencyCores,omitempty"`
	MemoryBytes      uint64 `json:"memoryBytes,omitempty"`
	DiskTotalBytes   uint64 `json:"diskTotalBytes,omitempty"`
	DiskFreeBytes    uint64 `json:"diskFreeBytes,omitempty"`
	Thermal          string `json:"thermal,omitempty"` // nominal | throttled | unknown
	// Battery is absent on desktops. Percent is 0-100; State is charging,
	// discharging, charged or ac (plugged in, not charging).
	BatteryPercent int    `json:"batteryPercent,omitempty"`
	BatteryState   string `json:"batteryState,omitempty"`
	// TunnelAddress is this host's secure-network address, so a receiving
	// connector can match the entry to the peer it sees in its own tunnel list.
	TunnelAddress string `json:"tunnelAddress,omitempty"`
	// PublicIP and Location are added by the coordinator from where the host's
	// connection came from (Cloudflare's view). A host never sends them; they
	// appear only on details received about OTHER hosts.
	PublicIP string `json:"publicIp,omitempty"`
	Location string `json:"location,omitempty"`
}

// Thermal states.
const (
	ThermalNominal   = "nominal"
	ThermalThrottled = "throttled"
	ThermalUnknown   = "unknown"
)

// DiskChangeThreshold is how far free space must move before a new report is
// worth sending. Smaller changes are noise for a status panel.
const DiskChangeThreshold = 1 << 30

// Material reports whether next differs from prev enough to be worth sending.
// Free space counts only when it moved by at least DiskChangeThreshold.
func Material(prev, next Info) bool {
	a, b := prev, next
	a.DiskFreeBytes, b.DiskFreeBytes = 0, 0
	a.BatteryPercent, b.BatteryPercent = 0, 0
	if a != b {
		return true
	}
	if abs(next.BatteryPercent-prev.BatteryPercent) >= BatteryChangeThreshold {
		return true
	}
	d := int64(next.DiskFreeBytes) - int64(prev.DiskFreeBytes)
	if d < 0 {
		d = -d
	}
	return d >= DiskChangeThreshold
}

// BatteryChangeThreshold is how many percentage points battery charge must move
// before a new report is sent.
const BatteryChangeThreshold = 5

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Runner runs a command and returns its stdout. Injectable for tests.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Collector caches the static part of Info.
type Collector struct {
	Run    Runner
	Statfs func(path string) (total, free uint64, err error)

	once   sync.Once
	static Info
}

// NewCollector returns a Collector using the real system.
func NewCollector() *Collector { return &Collector{Run: execRunner, Statfs: statfs} }

// Collect returns this host's details. It never fails: unknown values are zero.
func (c *Collector) Collect(ctx context.Context) Info {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c.once.Do(func() { c.static = c.readStatic(ctx) })
	info := c.static
	if c.Statfs != nil {
		if total, free, err := c.Statfs(dataVolume()); err == nil {
			info.DiskTotalBytes, info.DiskFreeBytes = total, free
		}
	}
	info.Thermal = c.thermal(ctx)
	if runtime.GOOS == "darwin" {
		if out, err := c.Run(ctx, "/usr/bin/pmset", "-g", "batt"); err == nil {
			info.BatteryPercent, info.BatteryState = parseBattery(out)
		}
	}
	return info
}

func (c *Collector) readStatic(ctx context.Context) Info {
	var info Info
	if runtime.GOOS != "darwin" {
		info.OS = runtime.GOOS
		info.Cores = runtime.NumCPU()
		return info
	}
	if out, err := c.Run(ctx, "/usr/sbin/sysctl", "machdep.cpu.brand_string", "hw.ncpu",
		"hw.perflevel0.physicalcpu", "hw.perflevel1.physicalcpu", "hw.memsize"); len(out) > 0 || err == nil {
		// sysctl exits non-zero when one key is missing (e.g. perflevels on
		// Intel) but still prints the others, so output is used either way.
		values := parseSysctl(out)
		info.Chip = values["machdep.cpu.brand_string"]
		info.Cores, _ = strconv.Atoi(values["hw.ncpu"])
		info.PerformanceCores, _ = strconv.Atoi(values["hw.perflevel0.physicalcpu"])
		info.EfficiencyCores, _ = strconv.Atoi(values["hw.perflevel1.physicalcpu"])
		info.MemoryBytes, _ = strconv.ParseUint(values["hw.memsize"], 10, 64)
	}
	if out, err := c.Run(ctx, "/usr/bin/sw_vers"); err == nil {
		info.OS = parseSwVers(out)
	}
	if out, err := c.Run(ctx, "/usr/sbin/system_profiler", "SPHardwareDataType"); err == nil {
		info.Model = profilerField(out, "Model Name")
		if info.Chip == "" {
			info.Chip = profilerField(out, "Chip")
		}
	}
	if info.Cores == 0 {
		info.Cores = runtime.NumCPU()
	}
	return info
}

func (c *Collector) thermal(ctx context.Context) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := c.Run(ctx, "/usr/bin/pmset", "-g", "therm")
	if err != nil {
		return ThermalUnknown
	}
	return parseTherm(out)
}

// parseSysctl reads "key: value" lines.
func parseSysctl(out []byte) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), ":")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values
}

// parseSwVers turns sw_vers output into "macOS 27.0 (27A5218g)".
func parseSwVers(out []byte) string {
	values := parseSysctl(out)
	name, version, build := values["ProductName"], values["ProductVersion"], values["BuildVersion"]
	if name == "" {
		name = "macOS"
	}
	if version == "" {
		return ""
	}
	if build != "" {
		return fmt.Sprintf("%s %s (%s)", name, version, build)
	}
	return name + " " + version
}

// profilerField returns the value of "Field: value" in system_profiler output.
func profilerField(out []byte, field string) string {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), ":")
		if ok && key == field {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// parseTherm reads `pmset -g therm`. A recorded CPU_Speed_Limit below 100
// means the kernel is throttling for heat; "No thermal warning level has been
// recorded" or a limit of 100 is nominal.
func parseTherm(out []byte) string {
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if limit, err := strconv.Atoi(values["CPU_Speed_Limit"]); err == nil && limit < 100 {
		return ThermalThrottled
	}
	if level, err := strconv.Atoi(values["Thermal_Level"]); err == nil && level > 0 {
		return ThermalThrottled
	}
	return ThermalNominal
}

// parseBattery reads `pmset -g batt`, e.g.
//
//	Now drawing from 'Battery Power'
//	 -InternalBattery-0 (id=1234)	85%; discharging; 4:12 remaining present: true
//
// A Mac without a battery prints no InternalBattery line: (0, "").
func parseBattery(out []byte) (int, string) {
	acPower := bytes.Contains(out, []byte("'AC Power'"))
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "InternalBattery") {
			continue
		}
		_, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		fields := strings.Split(rest, ";")
		percent, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(fields[0]), "%"))
		if err != nil || percent < 0 || percent > 100 {
			return 0, ""
		}
		state := ""
		if len(fields) > 1 {
			state = strings.TrimSpace(fields[1])
		}
		switch {
		case state == "charging", state == "discharging", state == "charged":
		case acPower:
			state = "ac"
		default:
			state = "discharging"
		}
		return percent, state
	}
	return 0, ""
}
