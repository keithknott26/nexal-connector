package sandbox

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestParseMemInfoRejectsMalformedMemTotal(t *testing.T) {
	for _, in := range []string{"MemTotal: lots kB\n", "MemTotal:\n", ""} {
		if _, err := ParseMemInfoTotalMB(in); err == nil {
			t.Fatalf("%q must fail", in)
		}
	}
}

func TestProcHostFactsReadsThisLinuxHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/meminfo")
	}
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip("no /proc/meminfo")
	}
	f, err := ProcHost{}.Facts(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if f.CPUs < 1 || f.MemoryMB == 0 || f.FreeDisk == 0 {
		t.Fatalf("implausible facts: %+v", f)
	}
	if f.OnBattery || f.PowerUnknown {
		t.Fatalf("a server has no battery: %+v", f)
	}
}

func TestNoVMHypervisorRefusesStart(t *testing.T) {
	var h NoVMHypervisor
	if _, err := h.Start(context.Background(), Spec{}); !errors.Is(err, errNoVMs) {
		t.Fatalf("Start err = %v", err)
	}
	if alive, err := h.Alive(context.Background(), Handle{}); alive || err != nil {
		t.Fatalf("Alive = %v, %v", alive, err)
	}
	if err := NoISO(context.Background(), "a", "b"); !errors.Is(err, errNoVMs) {
		t.Fatalf("NoISO err = %v", err)
	}
	if lid, known := NoLid(context.Background()); lid || known {
		t.Fatal("a server has no lid")
	}
}
