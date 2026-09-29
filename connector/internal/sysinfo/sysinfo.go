// Package sysinfo collects the host details shown for each computer in the
// neXal panel: OS, model, chip, cores, memory, disk and thermal state.
//
// Everything is read locally with fixed system tools at absolute paths and the
// standard library; nothing here needs administrator rights or cgo. The static
// part (OS, model, chip, cores, memory, disk size) is read once per process and
// cached; utilization, idle time, backup status, disk space and thermal state
// are re-read on each Collect.
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
	"math"
	"net"
	"net/netip"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Info is one host's details. Optional live metrics use pointers so a measured
// zero remains distinguishable from an unavailable measurement.
// JSON names are the wire contract with the coordinator's host.info frame.
type Info struct {
	LoadAverage1m           *float64 `json:"loadAverage1m,omitempty"`
	LoadAverage5m           *float64 `json:"loadAverage5m,omitempty"`
	LoadAverage15m          *float64 `json:"loadAverage15m,omitempty"`
	ExitNodeStatus          string   `json:"exitNodeStatus,omitempty"`
	CanaryStatus            string   `json:"canaryStatus,omitempty"`
	CanaryLastCheckedAt     string   `json:"canaryLastCheckedAt,omitempty"`
	HoneypotStatus          string   `json:"honeypotStatus,omitempty"`
	HoneypotLastTriggeredAt string   `json:"honeypotLastTriggeredAt,omitempty"`
	CPUUsagePercent         *float64 `json:"cpuUsagePercent,omitempty"`
	MemoryAvailableBytes    *uint64  `json:"memoryAvailableBytes,omitempty"`
	MemoryUsedBytes         *uint64  `json:"memoryUsedBytes,omitempty"`
	IdleSeconds             *uint64  `json:"idleSeconds,omitempty"`
	LastTimeMachineBackupAt string   `json:"lastTimeMachineBackupAt,omitempty"`
	ReportedAt              string   `json:"reportedAt,omitempty"`

	// Name is the computer's own name as its owner set it (System Settings →
	// General → About, e.g. "Keith's Mac mini"). The panel shows this rather than
	// the secure network's peer name, which is fixed at first registration from
	// the system hostname and can be stale (e.g. carried over by Migration Assistant).
	Name  string `json:"name,omitempty"`
	OS    string `json:"os,omitempty"`    // "macOS 27.0 (27A5218g)"
	Model string `json:"model,omitempty"` // "Mac mini"
	// Serial is the hardware serial number, shown only to the owner's own devices.
	Serial           string `json:"serial,omitempty"`
	Chip             string `json:"chip,omitempty"` // "Apple M4"
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
	// LANAddress is this computer's private IPv4 address on its local network
	// (e.g. 192.168.68.20), from its active physical interface.
	LANAddress string `json:"lanAddress,omitempty"`
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
	a.ReportedAt, b.ReportedAt = "", ""
	a.CanaryLastCheckedAt, b.CanaryLastCheckedAt = "", ""
	// Compare against the last transmitted values, so small changes accumulate.
	if floatChanged(a.CPUUsagePercent, b.CPUUsagePercent, 10) ||
		floatChanged(a.LoadAverage1m, b.LoadAverage1m, 0.5) ||
		floatChanged(a.LoadAverage5m, b.LoadAverage5m, 0.5) ||
		floatChanged(a.LoadAverage15m, b.LoadAverage15m, 0.5) ||
		bytesChanged(a.MemoryAvailableBytes, b.MemoryAvailableBytes, 256<<20) ||
		bytesChanged(a.MemoryUsedBytes, b.MemoryUsedBytes, 256<<20) || idleChanged(a.IdleSeconds, b.IdleSeconds) {
		return true
	}
	a.CPUUsagePercent, b.CPUUsagePercent = nil, nil
	a.LoadAverage1m, b.LoadAverage1m = nil, nil
	a.LoadAverage5m, b.LoadAverage5m = nil, nil
	a.LoadAverage15m, b.LoadAverage15m = nil, nil
	a.MemoryAvailableBytes, b.MemoryAvailableBytes = nil, nil
	a.MemoryUsedBytes, b.MemoryUsedBytes = nil, nil
	a.IdleSeconds, b.IdleSeconds = nil, nil
	if !reflect.DeepEqual(a, b) {
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
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	out := &limitedOutput{}
	cmd.Stdout = out
	err := cmd.Run()
	return out.Bytes(), err
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
	info.ReportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if c.Statfs != nil {
		if total, free, err := c.Statfs(dataVolume()); err == nil {
			info.DiskTotalBytes, info.DiskFreeBytes = total, free
		}
	}
	info.LANAddress = lanAddress()
	info.Thermal = c.thermal(ctx)
	if runtime.GOOS == "darwin" {
		if out, err := c.Run(ctx, "/usr/bin/pmset", "-g", "batt"); err == nil {
			info.BatteryPercent, info.BatteryState = parseBattery(out)
		}
	}
	c.collectLive(ctx, &info)
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
	if out, err := c.Run(ctx, "/usr/sbin/scutil", "--get", "ComputerName"); err == nil {
		info.Name = strings.TrimSpace(string(out))
	}
	if out, err := c.Run(ctx, "/usr/bin/sw_vers"); err == nil {
		info.OS = parseSwVers(out)
	}
	if out, err := c.Run(ctx, "/usr/sbin/system_profiler", "SPHardwareDataType"); err == nil {
		info.Model = profilerField(out, "Model Name")
		if serial := profilerField(out, "Serial Number (system)"); validSerial(serial) {
			info.Serial = serial
		}
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
	if strings.Contains(string(out), "No thermal warning level has been recorded") || values["CPU_Speed_Limit"] == "100" || values["Thermal_Level"] == "0" {
		return ThermalNominal
	}
	return ThermalUnknown
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

// LocalRanges are the address ranges that count as a computer's local-network
// (LAN) address, in order of preference, from the IANA special-purpose address
// registries (RFC 6890). The coordinator validates against the same list.
//
//	10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16  private networks (RFC 1918)
//	169.254.0.0/16                             IPv4 link-local, self-assigned (RFC 3927)
//	fc00::/7                                   IPv6 unique local (RFC 4193)
//
// 100.64.0.0/10 (shared address space, RFC 6598) is deliberately NOT listed: the
// secure network assigns tunnel addresses from it, so it is never a LAN address.
var LocalRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// localRank is the preference of ip among LocalRanges, or -1 if it is not local.
func localRank(ip netip.Addr) int {
	ip = ip.Unmap()
	for i, prefix := range LocalRanges {
		if prefix.Contains(ip) {
			return i
		}
	}
	return -1
}

// lanAddress returns this computer's most preferred local-network address (see
// LocalRanges) on an up, non-loopback, physical-looking interface (en*, eth*,
// wl*). Tunnel, bridge, VPN and AirDrop interfaces are skipped so the secure
// network's own address is never reported as the LAN address.
func lanAddress() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	return pickLANAddress(ifaces, func(i net.Interface) ([]net.Addr, error) { return i.Addrs() })
}

func pickLANAddress(ifaces []net.Interface, addrs func(net.Interface) ([]net.Addr, error)) string {
	best, bestRank := "", len(LocalRanges)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := iface.Name
		if !(strings.HasPrefix(name, "en") || strings.HasPrefix(name, "eth") || strings.HasPrefix(name, "wl")) {
			continue
		}
		list, err := addrs(iface)
		if err != nil {
			continue
		}
		for _, a := range list {
			prefix, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			if rank := localRank(ip); rank >= 0 && rank < bestRank {
				best, bestRank = ip.String(), rank
			}
		}
	}
	return best
}

// validSerial accepts Apple-style serial numbers only, matching the coordinator's check.
func validSerial(s string) bool {
	if len(s) < 8 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func floatChanged(a, b *float64, threshold float64) bool {
	if a == nil || b == nil {
		return (a == nil) != (b == nil)
	}
	return math.Abs(*a-*b) >= threshold
}
func bytesChanged(a, b *uint64, threshold uint64) bool {
	if a == nil || b == nil {
		return (a == nil) != (b == nil)
	}
	if *a > *b {
		return *a-*b >= threshold
	}
	return *b-*a >= threshold
}
func idleChanged(a, b *uint64) bool {
	if a == nil || b == nil {
		return (a == nil) != (b == nil)
	}
	return (*a < 60) != (*b < 60)
}
