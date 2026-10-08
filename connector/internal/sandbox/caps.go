package sandbox

import (
	"fmt"
	"time"
)

// Caps are the per-Mac limits. The Mac's owner adjusts them; Enabled is the
// per-Mac opt-in and is false by default, so a fresh install never runs guests.
type Caps struct {
	Enabled bool
	// MaxSandboxes is the most concurrent sandboxes (provisioning, running or
	// stopping). Default 5.
	MaxSandboxes int
	// MaxCPUFraction and MaxMemFraction bound the SUM of all sandboxes' virtual
	// CPUs and memory as a fraction of the host's. Default 0.5 each.
	MaxCPUFraction float64
	MaxMemFraction float64
	// DiskMarginBytes is the free space required beyond the image itself.
	// Default 10 GiB.
	DiskMarginBytes uint64
	// AllowOnBattery permits placement while on battery power. Default false.
	AllowOnBattery bool
	// StopGrace is how long an ACPI shutdown may take before the VM is killed.
	// Default 30 s.
	StopGrace time.Duration
	// FirstBootTimeout is how long to wait for the guest's first-boot report.
	// Default 15 min (own images run cloud-init setup, 2-5 min).
	FirstBootTimeout time.Duration
}

// DefaultCaps returns the §0 defaults (opt-in still off).
func DefaultCaps() Caps {
	return Caps{
		MaxSandboxes:    5,
		MaxCPUFraction:  0.5,
		MaxMemFraction:  0.5,
		DiskMarginBytes: 10 << 30,
		StopGrace:       30 * time.Second,
		// Budgeted at 90s daemon + 300s join + 60s address waits = 450s; that left
		// only 90s of slack on a loaded Mac and two VMs (sbx-e2264d25, sbx-71b28725)
		// both ran out the clock stuck between login and the mesh-join report. Give
		// it real headroom instead of guessing again.
		FirstBootTimeout: 15 * time.Minute,
	}
}

// Normalized fills zero or out-of-range fields with defaults and clamps
// fractions to (0, 0.9].
func (c Caps) Normalized() Caps {
	d := DefaultCaps()
	if c.MaxSandboxes <= 0 {
		c.MaxSandboxes = d.MaxSandboxes
	}
	if c.MaxCPUFraction <= 0 {
		c.MaxCPUFraction = d.MaxCPUFraction
	}
	if c.MaxCPUFraction > 0.9 {
		c.MaxCPUFraction = 0.9
	}
	if c.MaxMemFraction <= 0 {
		c.MaxMemFraction = d.MaxMemFraction
	}
	if c.MaxMemFraction > 0.9 {
		c.MaxMemFraction = 0.9
	}
	if c.DiskMarginBytes == 0 {
		c.DiskMarginBytes = d.DiskMarginBytes
	}
	if c.StopGrace <= 0 {
		c.StopGrace = d.StopGrace
	}
	if c.FirstBootTimeout <= 0 {
		c.FirstBootTimeout = d.FirstBootTimeout
	}
	return c
}

// HostFacts is what admission needs to know about this Mac right now.
type HostFacts struct {
	CPUs         int
	MemoryMB     uint64
	FreeDisk     uint64 // bytes free where sandbox disks live
	OnBattery    bool
	PowerUnknown bool // the power source could not be determined
}

// Usage is what already-admitted sandboxes hold.
type Usage struct {
	Count    int
	CPUs     int
	MemoryMB int
}

// RefusedError is an admission refusal. Code is stable for tests and the
// coordinator; the message is for the owner.
type RefusedError struct {
	Code string
	Msg  string
}

func (e *RefusedError) Error() string { return "sandbox refused (" + e.Code + "): " + e.Msg }

func refuse(code, format string, args ...any) error {
	return &RefusedError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Refusal codes.
const (
	RefuseDisabled     = "disabled"
	RefuseSize         = "size"
	RefuseCount        = "max_count"
	RefuseCPU          = "cpu"
	RefuseMemory       = "memory"
	RefuseDisk         = "disk"
	RefuseBattery      = "battery"
	RefusePowerUnknown = "power_unknown"
	RefuseExpired      = "expired"
)

// AdmitCaps decides whether one more sandbox of size req may start, given the
// current usage. Disk is checked separately (CheckDisk) once the image size is
// known. It is pure.
func AdmitCaps(c Caps, h HostFacts, u Usage, req Size) error {
	c = c.Normalized()
	if !c.Enabled {
		return refuse(RefuseDisabled, "this Mac is not set up to run throwaway hosts")
	}
	if err := ValidateSize(req); err != nil {
		return refuse(RefuseSize, "%v", err)
	}
	if u.Count >= c.MaxSandboxes {
		return refuse(RefuseCount, "already running %d of %d allowed", u.Count, c.MaxSandboxes)
	}
	maxCPU := int(float64(h.CPUs)*c.MaxCPUFraction + 1e-9)
	if u.CPUs+req.CPUs > maxCPU {
		if req.CPUs > maxCPU {
			return refuse(RefuseCPU, "this size needs %d CPUs but this Mac offers at most %d to instances; choose a smaller size", req.CPUs, maxCPU)
		}
		return refuse(RefuseCPU, "needs %d CPUs; %d of %d allowed are already in use", req.CPUs, u.CPUs, maxCPU)
	}
	maxMem := int(float64(h.MemoryMB)*c.MaxMemFraction + 1e-9)
	if u.MemoryMB+req.MemoryMB > maxMem {
		if req.MemoryMB > maxMem {
			return refuse(RefuseMemory, "this size needs %d MB of memory but this Mac offers at most %d MB to instances; choose a smaller size", req.MemoryMB, maxMem)
		}
		return refuse(RefuseMemory, "needs %d MB; %d of %d MB allowed are already in use", req.MemoryMB, u.MemoryMB, maxMem)
	}
	if !c.AllowOnBattery {
		if h.PowerUnknown {
			return refuse(RefusePowerUnknown, "cannot tell whether this Mac is on battery")
		}
		if h.OnBattery {
			return refuse(RefuseBattery, "this Mac is on battery power")
		}
	}
	return nil
}

// CheckDisk refuses when free space is below image size + margin. It is pure.
func CheckDisk(c Caps, freeDisk, imageBytes uint64) error {
	c = c.Normalized()
	need := imageBytes + c.DiskMarginBytes
	if freeDisk < need {
		return refuse(RefuseDisk, "needs %d MB free, %d MB available", need>>20, freeDisk>>20)
	}
	return nil
}
