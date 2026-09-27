package sysinfo

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
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
