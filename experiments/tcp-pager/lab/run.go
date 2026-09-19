package lab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"nexal/experiments/tcp-pager/pager"
)

type MemorySnapshot struct {
	CapturedAt    time.Time `json:"capturedAt"`
	Available     bool      `json:"measurementAvailable"`
	PhysicalBytes uint64    `json:"hwMemsizeBytes"`
	OS            string    `json:"os"`
	Architecture  string    `json:"architecture"`
}

func Snapshot(ctx context.Context) MemorySnapshot {
	s := MemorySnapshot{CapturedAt: time.Now().UTC(), OS: runtime.GOOS, Architecture: runtime.GOARCH}
	if runtime.GOOS != "darwin" {
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	out, err := cmd.Output()
	if err != nil {
		return s
	}
	s.PhysicalBytes, err = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	s.Available = err == nil && s.PhysicalBytes > 0
	return s
}

type CaseResult struct {
	Name                string           `json:"name"`
	DurationMS          int64            `json:"workloadDurationMs"`
	Paging              pager.Report     `json:"paging"`
	Error               string           `json:"error,omitempty"`
	ObservationWindowMS int              `json:"mappedCacheObservationWindowMs"`
	HostMemoryDuring    []MemorySnapshot `json:"hostMemoryDuringSession"`
}

type Report struct {
	Schema                           int            `json:"schemaVersion"`
	GoVersion                        string         `json:"goVersion"`
	SourceRevision                   string         `json:"sourceRevision,omitempty"`
	SourceModified                   string         `json:"sourceModified,omitempty"`
	Started                          time.Time      `json:"startedAt"`
	Endpoint                         string         `json:"endpoint"`
	NonLocalEndpoint                 bool           `json:"nonLocalEndpoint"`
	SeparatePhysicalMachinesAttested bool           `json:"separatePhysicalMachinesAttested"`
	Before                           MemorySnapshot `json:"hostMemoryBefore"`
	After                            MemorySnapshot `json:"hostMemoryAfter"`
	HostCounterUnchanged             *bool          `json:"hostPhysicalMemoryCounterUnchanged"`
	HostMemoryObservation            string         `json:"hostMemoryObservation"`
	AvailableHostSamples             int            `json:"availableHostMemorySamples"`
	Cases                            []CaseResult   `json:"cases"`
	PagingSuitePassed                bool           `json:"pagingSuitePassed"`
	NativeSuiteExecuted              bool           `json:"nativeSuiteExecuted"`
	HostRAMExpansionPassed           bool           `json:"hostRAMExpansionPassed"`
	GuestOSRAMExpansionPassed        bool           `json:"guestOSRAMExpansionPassed"`
	GPUMemoryExpansionPassed         bool           `json:"gpuMemoryExpansionPassed"`
	OSMemoryRequirement              string         `json:"osMemoryRequirement"`
	Limitations                      []string       `json:"limitations"`
}

// Receive creates its output directory exclusively, imports credentials, and
// always attempts to save a report after the manifest/import admission succeeds.
// Each workload deliberately starts a NEW disposable lab store, not a resumed
// memory session. There are no retries after ambiguous failures.
func Receive(ctx context.Context, bundle, output, helper string, portableOnly, loopback bool) (Report, error) {
	r := Report{Schema: 1, GoVersion: runtime.Version(), Started: time.Now().UTC(), OSMemoryRequirement: "NOT_IMPLEMENTED",
		Limitations: []string{
			"Fixed bare-metal guest only; no full guest operating system",
			"Not additional host macOS RAM or Metal/GPU memory",
			"Cache payload caps do not bound total process RSS",
			"Nonlocal IP is not physical-machine or same-LAN attestation",
			"Elapsed workload times are not network-only latency benchmarks",
			"Donor withdrawal aborts workloads; no recovery or persistence",
		}}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				r.SourceRevision = s.Value
			case "vcs.modified":
				r.SourceModified = s.Value
			}
		}
	}
	if !portableOnly && (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || helper == "") {
		return r, errors.New("native suite requires Apple-silicon macOS and an explicit helper")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return r, err
	}
	m, err := Import(bundle, filepath.Join(output, "credentials"), loopback)
	if err != nil {
		return r, err
	}
	r.Endpoint = m.Endpoint
	local, err := IsLocal(m.Endpoint)
	if err != nil {
		return r, err
	}
	r.NonLocalEndpoint = !local
	if local && !loopback {
		return r, errors.New("receiver refuses its own address for two-computer acceptance")
	}
	tlsConfig, err := pager.LoadTLS(filepath.Join(output, "credentials"), false)
	if err != nil {
		return r, err
	}
	r.Before = Snapshot(ctx)
	cases := []struct {
		name   string
		slots  int
		native bool
	}{
		{"portable-baseline", 4, false},
	}
	if !portableOnly {
		cases = append(cases, struct {
			name   string
			slots  int
			native bool
		}{"native-4-page-cache", 4, true},
			struct {
				name   string
				slots  int
				native bool
			}{"native-1-page-cache-stress", 1, true},
			struct {
				name   string
				slots  int
				native bool
			}{"native-repeat-fresh-store", 4, true})
	}
	for _, tc := range cases {
		var cr CaseResult
		cr.Name = tc.name
		wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		c, e := pager.Dial(wctx, m.Endpoint, tlsConfig)
		if e == nil {
			if c.Pages() != Pages {
				e = errors.New("donor page capacity differs from manifest")
			} else {
				start := time.Now()
				if tc.native {
					cr.ObservationWindowMS = 1000
					observed := observeMemory(wctx)
					cr.Paging, e = pager.NativeObserved(wctx, c, tc.slots, helper)
					cr.HostMemoryDuring = observed()
				} else {
					cr.Paging, e = pager.Portable(wctx, c, tc.slots)
				}
				cr.DurationMS = time.Since(start).Milliseconds()
				if e == nil {
					e = CheckResult(cr.Paging, tc.slots, tc.native)
				}
			}
			c.Close()
		}
		cancel()
		if e != nil {
			cr.Error = e.Error()
			err = fmt.Errorf("%s failed: %w", tc.name, e)
		}
		r.Cases = append(r.Cases, cr)
		if err != nil {
			break
		}
	}
	r.After = Snapshot(ctx)
	if r.Before.Available && r.After.Available {
		equal := r.Before.PhysicalBytes == r.After.PhysicalBytes
		r.HostCounterUnchanged = &equal
	}
	r.HostMemoryObservation, r.AvailableHostSamples = classifyMemory(r)
	r.PagingSuitePassed = err == nil && len(r.Cases) == len(cases)
	r.NativeSuiteExecuted = r.PagingSuitePassed && !portableOnly
	// Even a changed hw.memsize counter is insufficient evidence of usable
	// memory. No existing OS allocator/application integration is implemented.
	if e := WriteJSON(filepath.Join(output, "report.json"), r); e != nil {
		return r, fmt.Errorf("save report: %w", e)
	}
	return r, err
}

func classifyMemory(r Report) (string, int) {
	all := []MemorySnapshot{r.Before, r.After}
	for _, c := range r.Cases {
		all = append(all, c.HostMemoryDuring...)
	}
	var first uint64
	count := 0
	changed := false
	for _, s := range all {
		if !s.Available {
			continue
		}
		if count == 0 {
			first = s.PhysicalBytes
		} else if first != s.PhysicalBytes {
			changed = true
		}
		count++
	}
	if changed {
		return "counter_changed_requires_investigation_not_proof_of_expansion", count
	}
	if count < 2 {
		return "insufficient_available_measurements", count
	}
	return "unchanged_in_available_samples", count
}

// observeMemory retains at most the last 16 samples. Stop waits for the
// observer to exit, preventing leaks or races when a workload fails.
func observeMemory(ctx context.Context) func() []MemorySnapshot {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan []MemorySnapshot, 1)
	go func() {
		var samples []MemorySnapshot
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- samples
				return
			case <-ticker.C:
				s := Snapshot(ctx)
				if len(samples) == 16 {
					copy(samples, samples[1:])
					samples = samples[:15]
				}
				samples = append(samples, s)
			}
		}
	}()
	return func() []MemorySnapshot { cancel(); return <-done }
}

func CheckResult(r pager.Report, slots int, native bool) error {
	if !r.Verified || r.LogicalBytes != Pages*pager.PageSize || r.VerifiedBytes != Pages*pager.PageSize ||
		r.CachePayloadLimitBytes != slots*pager.PageSize || r.Cache.PeakResidentPages != slots ||
		r.Cache.Faults != 2*Pages || r.Cache.Evictions != uint64(2*(Pages-slots)) ||
		r.Transport.Gets != 2*Pages || r.Transport.Puts != Pages ||
		r.Transport.PageBytesReceived != 2*Pages*pager.PageSize ||
		r.Transport.PageBytesSent != Pages*pager.PageSize || r.NativeHVFExecuted != native ||
		r.HostRAMExpanded || r.MacOSGuestBooted || r.GPUMemoryExpanded {
		return errors.New("result does not match bounded acceptance invariants")
	}
	return nil
}
