package sandbox

import (
	"errors"
	"testing"
)

func okHost() HostFacts {
	return HostFacts{CPUs: 10, MemoryMB: 32768, FreeDisk: 200 << 30}
}

func enabledCaps() Caps {
	c := DefaultCaps()
	c.Enabled = true
	return c
}

func code(err error) string {
	var r *RefusedError
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

func TestAdmitCapsDisabledByDefault(t *testing.T) {
	err := AdmitCaps(DefaultCaps(), okHost(), Usage{}, Size{CPUs: 2, MemoryMB: 2048, DiskGB: 10})
	if code(err) != RefuseDisabled {
		t.Fatalf("want disabled, got %v", err)
	}
}

func TestAdmitCapsOK(t *testing.T) {
	if err := AdmitCaps(enabledCaps(), okHost(), Usage{}, Size{CPUs: 4, MemoryMB: 8192, DiskGB: 40}); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitCapsMaxCount(t *testing.T) {
	err := AdmitCaps(enabledCaps(), okHost(), Usage{Count: 5}, Size{CPUs: 1, MemoryMB: 512, DiskGB: 5})
	if code(err) != RefuseCount {
		t.Fatalf("want max_count, got %v", err)
	}
	if err := AdmitCaps(enabledCaps(), okHost(), Usage{Count: 4}, Size{CPUs: 1, MemoryMB: 512, DiskGB: 5}); err != nil {
		t.Fatalf("4 of 5 should pass: %v", err)
	}
}

func TestAdmitCapsCPUHalf(t *testing.T) {
	// 10 CPUs -> 5 allowed in total.
	if err := AdmitCaps(enabledCaps(), okHost(), Usage{Count: 1, CPUs: 1}, Size{CPUs: 4, MemoryMB: 512, DiskGB: 5}); err != nil {
		t.Fatalf("5 of 5 should pass: %v", err)
	}
	err := AdmitCaps(enabledCaps(), okHost(), Usage{Count: 1, CPUs: 2}, Size{CPUs: 4, MemoryMB: 512, DiskGB: 5})
	if code(err) != RefuseCPU {
		t.Fatalf("want cpu, got %v", err)
	}
}

func TestAdmitCapsMemoryHalf(t *testing.T) {
	// 32768 MB -> 16384 allowed.
	err := AdmitCaps(enabledCaps(), okHost(), Usage{Count: 1, MemoryMB: 10000}, Size{CPUs: 1, MemoryMB: 8192, DiskGB: 5})
	if code(err) != RefuseMemory {
		t.Fatalf("want memory, got %v", err)
	}
}

func TestAdmitCapsBattery(t *testing.T) {
	h := okHost()
	h.OnBattery = true
	req := Size{CPUs: 1, MemoryMB: 512, DiskGB: 5}
	if code(AdmitCaps(enabledCaps(), h, Usage{}, req)) != RefuseBattery {
		t.Fatal("battery must be refused")
	}
	h.OnBattery, h.PowerUnknown = false, true
	if code(AdmitCaps(enabledCaps(), h, Usage{}, req)) != RefusePowerUnknown {
		t.Fatal("unknown power must be refused")
	}
	c := enabledCaps()
	c.AllowOnBattery = true
	h.PowerUnknown, h.OnBattery = false, true
	if err := AdmitCaps(c, h, Usage{}, req); err != nil {
		t.Fatalf("owner allowed battery: %v", err)
	}
}

func TestAdmitCapsBadSize(t *testing.T) {
	if code(AdmitCaps(enabledCaps(), okHost(), Usage{}, Size{})) != RefuseSize {
		t.Fatal("zero size must be refused")
	}
}

func TestCheckDisk(t *testing.T) {
	c := DefaultCaps()
	if code(CheckDisk(c, 5<<30, 1<<30)) != RefuseDisk {
		t.Fatal("5 GiB free < 1 GiB + 10 GiB must refuse")
	}
	if err := CheckDisk(c, 12<<30, 1<<30); err != nil {
		t.Fatal(err)
	}
	if code(CheckDisk(c, 11<<30-1, 1<<30)) != RefuseDisk {
		t.Fatal("one byte short must refuse")
	}
}

func TestNormalized(t *testing.T) {
	c := Caps{MaxCPUFraction: 5}.Normalized()
	if c.MaxSandboxes != 5 || c.MaxCPUFraction != 0.9 || c.MaxMemFraction != 0.5 {
		t.Fatalf("unexpected %+v", c)
	}
}

func TestAdmitCapsContainerSizeFitsWhereVMDoesNot(t *testing.T) {
	// A 2-CPU, 2 GB Mac offers 1 CPU / 1024 MB at the default fractions: the
	// smallest VM (2 CPUs, 2 GB) is refused, the default container size fits.
	c := DefaultCaps()
	c.Enabled, c.AllowOnBattery = true, true
	h := HostFacts{CPUs: 2, MemoryMB: 2048}
	if err := AdmitCaps(c, h, Usage{}, Size{CPUs: 2, MemoryMB: 2048, DiskGB: 10}); err == nil {
		t.Fatal("VM size must not fit")
	}
	if err := AdmitCaps(c, h, Usage{}, devDefaultSize); err != nil {
		t.Fatalf("container default must fit: %v", err)
	}
	if devDefaultSize != (Size{CPUs: 1, MemoryMB: 512, DiskGB: 4}) {
		t.Fatalf("devDefaultSize must match the coordinator's small container preset: %+v", devDefaultSize)
	}
}
