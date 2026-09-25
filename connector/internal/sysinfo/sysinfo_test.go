package sysinfo

import (
	"context"
	"errors"
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
	if runtime.GOOS == "darwin" && calls-first != 1 {
		t.Fatalf("static part re-read: %d extra calls", calls-first-1)
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
