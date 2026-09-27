package sysinfo

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLiveParsers(t *testing.T) {
	cpu := parseCPU([]byte("CPU usage: 20.0% user, 10.0% sys, 70.0% idle\nCPU usage: 1.0% user, 2.0% sys, 97.0% idle"))
	if cpu == nil || *cpu != 3 {
		t.Fatal("must use the latest measured CPU interval", cpu)
	}
	if parseCPU([]byte("CPU usage: 1% user, 2% sys, 101% idle")) != nil {
		t.Fatal("invalid CPU accepted")
	}
	if parseCPU([]byte("unavailable")) != nil {
		t.Fatal("unknown CPU should not be zero")
	}
	m := parseAvailable([]byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10.\nPages speculative: 2.\n"), 1<<30)
	if m == nil || *m != 12*16384 {
		t.Fatal("invalid free memory", m)
	}
	if parseAvailable([]byte("page size of 16384 bytes\nPages free: 999999999999.\nPages speculative: 2.\n"), 1<<30) != nil {
		t.Fatal("overflow accepted")
	}
	if got := parseBackup([]byte("/Volumes/private-owner/Backups.backupdb/Secret Mac/2025-01-02-030405")); got == "" || strings.Contains(got, "Secret") {
		t.Fatal("timestamp not sanitized", got)
	}
	if parseBackup([]byte("Permission denied")) != "" {
		t.Fatal("error mistaken for backup")
	}
}
func TestMaterialUsesMetricValues(t *testing.T) {
	x, y := 12.5, 12.5
	a, b := Info{CPUUsagePercent: &x, ReportedAt: "one"}, Info{CPUUsagePercent: &y, ReportedAt: "two"}
	if Material(a, b) {
		t.Fatal("pointer allocation and timestamp are not a material metric change")
	}
	y = 30
	if !Material(a, b) {
		t.Fatal("CPU change was dropped")
	}
}

func TestLoadAverageParsingAndMaterialChanges(t *testing.T) {
	a, b, c := parseLoadAverage([]byte("{ 0.00 1.25 2.50 }\n"))
	if a == nil || *a != 0 || b == nil || *b != 1.25 || c == nil || *c != 2.5 {
		t.Fatal("load averages not parsed")
	}
	for _, bad := range []string{"{ NaN 1 2 }", "{ +Inf 1 2 }", "{ -1 2 3 }", "{ 1000001 2 3 }", "{ 1 2 }", "vm.loadavg: { 1 2 3 }"} {
		if x, _, _ := parseLoadAverage([]byte(bad)); x != nil {
			t.Fatalf("accepted malformed load %q", bad)
		}
	}
	before := Info{LoadAverage1m: a, LoadAverage5m: b, LoadAverage15m: c}
	clone, _, _ := parseLoadAverage([]byte("{ 0 1.25 2.50 }"))
	after := before
	after.LoadAverage1m = clone
	if Material(before, after) {
		t.Fatal("equal pointer-backed values caused update")
	}
	changed := 0.5
	after.LoadAverage1m = &changed
	if !Material(before, after) {
		t.Fatal("real load change was not reported")
	}
	raw, err := json.Marshal(before)
	if err != nil || !strings.Contains(string(raw), `"loadAverage1m":0`) {
		t.Fatal("measured zero load omitted", string(raw), err)
	}
}

func TestMaterialIgnoresSamplingNoise(t *testing.T) {
	cpuA, cpuB := 10.0, 19.9
	loadA, loadB := 1.0, 1.49
	memA, memB := uint64(1<<30), uint64(1<<30)+255<<20
	idleA, idleB := uint64(61), uint64(121)
	a := Info{CPUUsagePercent: &cpuA, LoadAverage1m: &loadA, MemoryUsedBytes: &memA, IdleSeconds: &idleA}
	b := Info{CPUUsagePercent: &cpuB, LoadAverage1m: &loadB, MemoryUsedBytes: &memB, IdleSeconds: &idleB}
	if Material(a, b) {
		t.Fatal("small metric movements sent a frame")
	}
	cpuB = 20
	if !Material(a, b) {
		t.Fatal("ten-point CPU change suppressed")
	}
	cpuB = 10
	memB = memA + (256 << 20)
	if !Material(a, b) {
		t.Fatal("significant memory change suppressed")
	}
	memB = memA
	idleB = 0
	if !Material(a, b) {
		t.Fatal("owner activity transition suppressed")
	}
	b = a
	b.CPUUsagePercent = nil
	if !Material(a, b) {
		t.Fatal("metric availability transition suppressed")
	}
}
