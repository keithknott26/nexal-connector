package sandbox

import (
	"context"
	"sync"
	"time"
)

// Provisioning steps reported to the coordinator, in order. The app shows the
// same list; unknown ids are ignored there.
const (
	StepCheck    = "check"
	StepDownload = "download"
	StepConvert  = "convert"
	StepDisk     = "disk"
	StepSeed     = "seed"
	StepBoot     = "boot"
	StepJoin     = "join"
)

// ProgressFunc reports a provisioning step and an overall percent (0..100).
type ProgressFunc func(step string, percent int)

type progressKey struct{}

// WithProgress attaches a ProgressFunc to ctx for the image store to call.
func WithProgress(ctx context.Context, f ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, f)
}

func progressOf(ctx context.Context) ProgressFunc {
	if f, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && f != nil {
		return f
	}
	return func(string, int) {}
}

// throttle drops reports that change neither the step nor the percent by at
// least 3 points, and anything faster than once every 2 s within a step.
func throttle(next ProgressFunc) ProgressFunc {
	var mu sync.Mutex
	var step string
	var pct int
	var at time.Time
	return func(s string, p int) {
		mu.Lock()
		if s == step && (p-pct < 3 || time.Since(at) < 2*time.Second) {
			mu.Unlock()
			return
		}
		step, pct, at = s, p, time.Now()
		mu.Unlock()
		next(s, p)
	}
}

// reportProgress is one best-effort report: it never retries and never fails a boot.
func (m *Manager) reportProgress(id, step string, pct int) {
	m.mu.Lock()
	coord, hostID, base := m.coord, m.hostID, m.ctx
	m.mu.Unlock()
	if coord == nil || hostID == "" || base == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(base), 5*time.Second)
	defer cancel()
	_ = coord.ReportSandboxState(ctx, hostID, StateReport{SandboxID: id, State: StateProvisioning, Step: step, Percent: pct})
}
