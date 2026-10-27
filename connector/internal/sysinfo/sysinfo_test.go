package sysinfo

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"testing"
)

func TestParsers(t *testing.T) {
	if got := parseSwVers([]byte("ProductName:\t\tmacOS\nProductVersion:\t\t27.0\nBuildVersion:\t\t27A5218g\n")); got != "macOS 27.0 (27A5218g)" {
		t.Fatalf("sw_vers: %q", got)
	}
	v := parseSysctl([]byte("machdep.cpu.brand_string: Apple M4\nhw.ncpu: 10\nhw.memsize: 17179869184\n"))
	if v["machdep.cpu.brand_string"] != "Apple M4" || v["hw.ncpu"] != "10" || v["hw.memsize"] != "17179869184" {
		t.Fatalf("sysctl: %v", v)
	}
	hw := []byte("Hardware:\n\n    Hardware Overview:\n\n      Model Name: Mac mini\n      Chip: Apple M4\n")
	if profilerField(hw, "Model Name") != "Mac mini" || profilerField(hw, "Chip") != "Apple M4" {
		t.Fatal("system_profiler")
	}
	if parseTherm([]byte("Note: No thermal warning level has been recorded\n")) != ThermalNominal {
		t.Fatal("nominal")
	}
	if parseTherm([]byte("CPU Power notify\n\tCPU_Scheduler_Limit \t= 100\n\tCPU_Available_CPUs \t= 10\n\tCPU_Speed_Limit \t= 70\n")) != ThermalThrottled {
		t.Fatal("throttled")
	}
}

func TestMaterial(t *testing.T) {
	base := Info{OS: "macOS 27.0", DiskFreeBytes: 100 << 30, Thermal: ThermalNominal}
	small := base
	small.DiskFreeBytes -= 100 << 20
	if Material(base, small) {
		t.Fatal("100 MiB is not material")
	}
	big := base
	big.DiskFreeBytes -= 2 << 30
	if !Material(base, big) {
		t.Fatal("2 GiB is material")
	}
	hot := base
	hot.Thermal = ThermalThrottled
	if !Material(base, hot) {
		t.Fatal("thermal change is material")
	}
}

func TestCollectorCachesStaticAndNeverFails(t *testing.T) {
	calls := 0
	c := &Collector{
		Run:    func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, errors.New("absent") },
		Statfs: func(string) (uint64, uint64, error) { return 500 << 30, 200 << 30, nil },
	}
	a := c.Collect(context.Background())
	first := calls
	b := c.Collect(context.Background())
	if a.DiskTotalBytes != 500<<30 || b.DiskFreeBytes != 200<<30 || a.Cores == 0 {
		t.Fatalf("unexpected info: %+v", a)
	}
	if runtime.GOOS == "darwin" && calls-first != 7 {
		t.Fatalf("static part re-read: %d extra calls", calls-first-7)
	}
}

func TestParseBattery(t *testing.T) {
	p, s := parseBattery([]byte("Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1234)\t85%; discharging; 4:12 remaining present: true\n"))
	if p != 85 || s != "discharging" {
		t.Fatalf("got %d %q", p, s)
	}
	p, s = parseBattery([]byte("Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n"))
	if p != 100 || s != "charged" {
		t.Fatalf("got %d %q", p, s)
	}
	p, s = parseBattery([]byte("Now drawing from 'AC Power'\n"))
	if p != 0 || s != "" {
		t.Fatalf("desktop got %d %q", p, s)
	}
}

func TestParseBatteryEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		percent int
		state   string
	}{
		{"line without tab is skipped", "Now drawing from 'Battery Power'\n -InternalBattery-0 85%; discharging\n", 0, ""},
		{"non-numeric percent", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t(no estimate)%; discharging\n", 0, ""},
		{"percent above 100", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t101%; discharging\n", 0, ""},
		{"negative percent", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t-5%; discharging\n", 0, ""},
		{"missing state on AC", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t50%\n", 50, "ac"},
		{"missing state on battery", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t50%\n", 50, "discharging"},
		{"unknown state on AC", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t80%; finishing charge; 0:10 remaining\n", 80, "ac"},
		{"charging is kept", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t42%; charging; 1:00 remaining\n", 42, "charging"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, s := parseBattery([]byte(tc.in))
			if p != tc.percent || s != tc.state {
				t.Fatalf("got %d %q, want %d %q", p, s, tc.percent, tc.state)
			}
		})
	}
}

func TestPickLANAddressSkipsTunnelsAndPublicAddresses(t *testing.T) {
	ifaces := []net.Interface{
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},
		{Name: "utun4", Flags: net.FlagUp},
		{Name: "en1", Flags: 0},
		{Name: "en0", Flags: net.FlagUp},
	}
	addrs := map[string][]net.Addr{
		"lo0":   {&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}},
		"utun4": {&net.IPNet{IP: net.ParseIP("100.86.178.78"), Mask: net.CIDRMask(16, 32)}},
		"en1":   {&net.IPNet{IP: net.ParseIP("10.0.0.9"), Mask: net.CIDRMask(8, 32)}},
		"en0": {&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
			&net.IPNet{IP: net.ParseIP("192.168.68.56"), Mask: net.CIDRMask(22, 32)}},
	}
	got := pickLANAddress(ifaces, func(i net.Interface) ([]net.Addr, error) { return addrs[i.Name], nil })
	if got != "192.168.68.56" {
		t.Fatalf("got %q", got)
	}
}

func TestLocalRangesCoverEveryLANFamilyButNotTheTunnelRange(t *testing.T) {
	for _, local := range []string{"10.1.2.3", "172.16.0.1", "172.31.255.254", "192.168.68.56", "169.254.10.20", "fd12:3456::1"} {
		if localRank(netip.MustParseAddr(local)) < 0 {
			t.Errorf("%s should be local", local)
		}
	}
	for _, other := range []string{"100.86.178.78", "73.1.2.3", "172.32.0.1", "8.8.8.8", "fe80::1", "2001:db8::1"} {
		if localRank(netip.MustParseAddr(other)) >= 0 {
			t.Errorf("%s must not be local", other)
		}
	}
}

func TestPickLANAddressPrefersIPv4PrivateOverLinkLocalAndULA(t *testing.T) {
	ifaces := []net.Interface{{Name: "en0", Flags: net.FlagUp}, {Name: "en1", Flags: net.FlagUp}}
	addrs := map[string][]net.Addr{
		"en0": {&net.IPNet{IP: net.ParseIP("fd00::5"), Mask: net.CIDRMask(64, 128)},
			&net.IPNet{IP: net.ParseIP("169.254.3.4"), Mask: net.CIDRMask(16, 32)}},
		"en1": {&net.IPNet{IP: net.ParseIP("10.20.30.40"), Mask: net.CIDRMask(8, 32)}},
	}
	if got := pickLANAddress(ifaces, func(i net.Interface) ([]net.Addr, error) { return addrs[i.Name], nil }); got != "10.20.30.40" {
		t.Fatalf("got %q", got)
	}
}

func TestSerialFromProfiler(t *testing.T) {
	hw := []byte("Hardware Overview:\n      Model Name: Mac mini\n      Serial Number (system): C07ABC123XYZ\n")
	if got := profilerField(hw, "Serial Number (system)"); !validSerial(got) || got != "C07ABC123XYZ" {
		t.Fatalf("serial = %q", got)
	}
	for _, bad := range []string{"", "short", "c07abc123xyz", "C07ABC 123XYZ", "C07ABC123XYZ0123456789"} {
		if validSerial(bad) {
			t.Fatalf("validSerial(%q) = true", bad)
		}
	}
}

func TestExecRunnerScrubsEnvAndReportsFailure(t *testing.T) {
	t.Setenv("NEXAL_SYSINFO_LEAK", "secret")
	out, err := execRunner(context.Background(), "/usr/bin/env")
	if err != nil {
		t.Skipf("/usr/bin/env unavailable: %v", err)
	}
	if strings.Contains(string(out), "NEXAL_SYSINFO_LEAK") {
		t.Fatalf("environment leaked into child: %q", out)
	}
	if !strings.Contains(string(out), "LC_ALL=C") {
		t.Fatalf("expected pinned locale in child env: %q", out)
	}
	if _, err := execRunner(context.Background(), "/nonexistent/nexal-binary"); err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestNewCollectorWiresRealRunnerAndStatfs(t *testing.T) {
	c := NewCollector()
	if c.Run == nil || c.Statfs == nil {
		t.Fatalf("NewCollector left hooks unset: %+v", c)
	}
}

func TestParseThermEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"empty output is unknown", "", ThermalUnknown},
		{"unrelated output is unknown", "something else entirely\n", ThermalUnknown},
		{"speed limit 100 is nominal", "CPU_Speed_Limit \t= 100\n", ThermalNominal},
		{"thermal level 0 is nominal", "Thermal_Level = 0\n", ThermalNominal},
		{"positive thermal level is throttled", "Thermal_Level = 2\n", ThermalThrottled},
		{"non-numeric limit is unknown", "CPU_Speed_Limit = fast\n", ThermalUnknown},
	}
	for _, tc := range cases {
		if got := parseTherm([]byte(tc.out)); got != tc.want {
			t.Errorf("%s: parseTherm = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseSwVersEdgeCases(t *testing.T) {
	if got := parseSwVers([]byte("ProductName: macOS\nBuildVersion: 27A1\n")); got != "" {
		t.Fatalf("missing version should yield empty, got %q", got)
	}
	if got := parseSwVers([]byte("ProductVersion: 27.0\n")); got != "macOS 27.0" {
		t.Fatalf("missing name and build should default to macOS, got %q", got)
	}
	if got := parseSwVers(nil); got != "" {
		t.Fatalf("empty output: %q", got)
	}
}

func TestAbs(t *testing.T) {
	for in, want := range map[int]int{-7: 7, 0: 0, 7: 7} {
		if got := abs(in); got != want {
			t.Fatalf("abs(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestMaterialBatteryThresholdIsSymmetricAndInclusive(t *testing.T) {
	base := Info{BatteryPercent: 50}
	for _, tc := range []struct {
		name string
		pct  int
		want bool
	}{
		{"just under on the way down", 50 - BatteryChangeThreshold + 1, false},
		{"exact threshold down", 50 - BatteryChangeThreshold, true},
		{"just under on the way up", 50 + BatteryChangeThreshold - 1, false},
		{"exact threshold up", 50 + BatteryChangeThreshold, true},
	} {
		next := base
		next.BatteryPercent = tc.pct
		if got := Material(base, next); got != tc.want {
			t.Fatalf("%s: Material = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseSysctlEdgeCases(t *testing.T) {
	if got := parseSysctl(nil); len(got) != 0 {
		t.Fatalf("empty input: %v", got)
	}
	v := parseSysctl([]byte("no separator here\n  spaced key  :  padded value  \nurl: http://host:80/x\n: nokey\n"))
	if _, ok := v["no separator here"]; ok {
		t.Fatalf("line without colon must be skipped: %v", v)
	}
	if v["spaced key"] != "padded value" {
		t.Fatalf("key and value must be trimmed: %v", v)
	}
	if v["url"] != "http://host:80/x" {
		t.Fatalf("only the first colon splits: %v", v)
	}
	if val, ok := v[""]; !ok || val != "nokey" {
		t.Fatalf("empty key is kept as-is: %v", v)
	}
}

func TestProfilerFieldMatchesExactKeyOnly(t *testing.T) {
	out := []byte("Hardware:\n    Chip Name: M4\n    Chip: Apple M4 Pro\n    Memory: 24 GB\n")
	if got := profilerField(out, "Chip"); got != "Apple M4 Pro" {
		t.Fatalf("Chip: %q", got)
	}
	if got := profilerField(out, "Missing"); got != "" {
		t.Fatalf("missing field: %q", got)
	}
	if got := profilerField(nil, "Chip"); got != "" {
		t.Fatalf("empty output: %q", got)
	}
}

func TestValidSerialLengthBoundaries(t *testing.T) {
	for s, want := range map[string]bool{
		"ABCDEFG":               false, // 7
		"ABCDEFGH":              true,  // 8
		"ABCDEFGHIJKLMNOPQRST":  true,  // 20
		"ABCDEFGHIJKLMNOPQRSTU": false, // 21
		"ABC-EFGH":              false,
	} {
		if got := validSerial(s); got != want {
			t.Errorf("validSerial(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestLocalRankUnmapsIPv4AndRejectsPublic(t *testing.T) {
	if got := localRank(netip.MustParseAddr("::ffff:192.168.1.5")); got != 2 {
		t.Fatalf("mapped 192.168.1.5 rank = %d, want 2", got)
	}
	for _, s := range []string{"8.8.8.8", "172.32.0.1", "100.64.0.1", "2001:db8::1"} {
		if got := localRank(netip.MustParseAddr(s)); got != -1 {
			t.Errorf("localRank(%s) = %d, want -1", s, got)
		}
	}
}

func TestNonDarwinStaticAndThermalSkipExternalCommands(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS shells out to sysctl/pmset by design")
	}
	c := &Collector{
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("non-darwin hosts must not run macOS tools")
			return nil, nil
		},
	}
	info := c.readStatic(context.Background())
	if info.OS != runtime.GOOS || info.Cores != runtime.NumCPU() || info.Chip != "" {
		t.Fatalf("unexpected static info: %+v", info)
	}
	if got := c.thermal(context.Background()); got != "" {
		t.Fatalf("thermal = %q, want empty off macOS", got)
	}
}

func TestNonDarwinStaticAndThermalSkipSystemTools(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin runs the real system tools")
	}
	c := &Collector{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("no system tool may run off darwin")
		return nil, nil
	}}
	info := c.readStatic(context.Background())
	if info.OS != runtime.GOOS || info.Cores != runtime.NumCPU() || info.Chip != "" || info.Serial != "" {
		t.Fatalf("static info = %+v", info)
	}
	if got := c.thermal(context.Background()); got != "" {
		t.Fatalf("thermal = %q, want empty off darwin", got)
	}
}
