package sandbox

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Pieces for running the Manager on a Linux server as a managed, containers-only
// runner (neXal storage). The macOS defaults (sysctl, pmset, hdiutil, ioreg,
// Virtualization.framework) do not exist there.

// ProcHost reads host facts on Linux: CPUs from the runtime, memory from
// /proc/meminfo, free disk from df. A server has no battery.
type ProcHost struct{}

// Facts implements Host.
func (ProcHost) Facts(ctx context.Context, diskPath string) (HostFacts, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f := HostFacts{CPUs: runtime.NumCPU()}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return f, errors.New("cannot read host memory")
	}
	if f.MemoryMB, err = ParseMemInfoTotalMB(string(b)); err != nil {
		return f, err
	}
	df := "/usr/bin/df"
	if _, err := os.Stat(df); err != nil {
		df = "/bin/df"
	}
	out, err := execRunner(ctx, df, "-Pk", diskPath)
	if err != nil {
		return f, errors.New("cannot read free disk space")
	}
	if f.FreeDisk, err = ParseDFFree(string(out)); err != nil {
		return f, err
	}
	return f, nil
}

// ParseMemInfoTotalMB returns MemTotal from /proc/meminfo in MiB. It is pure.
func ParseMemInfoTotalMB(s string) (uint64, error) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				break
			}
			return kb >> 10, nil
		}
	}
	return 0, errors.New("cannot read host memory")
}

// NoVMHypervisor refuses VMs (Linux QEMU/KVM is ticket 5). The Manager's
// ContainersOnly option refuses VM tasks before they get here.
type NoVMHypervisor struct{}

var errNoVMs = errors.New("this host runs dev containers only")

func (NoVMHypervisor) Start(context.Context, Spec) (Handle, error) { return Handle{}, errNoVMs }
func (NoVMHypervisor) Stop(context.Context, Handle) error          { return nil }
func (NoVMHypervisor) Kill(context.Context, Handle) error          { return nil }
func (NoVMHypervisor) Alive(context.Context, Handle) (bool, error) { return false, nil }

// NoISO refuses seed images (VM-only).
func NoISO(context.Context, string, string) error { return errNoVMs }

// NoLid is a server: there is no lid, so the sleep heuristic never fires.
func NoLid(context.Context) (bool, bool) { return false, false }
