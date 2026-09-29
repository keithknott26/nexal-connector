package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

func (a *Agent) requiredMemoryLocked() uint64 {
	if a.runtimeMemory != 0 {
		return a.runtimeMemory
	}
	return WorkloadMemoryBytes
}

// ReservePrivateRuntime acquires the agent's exclusive execution slot using a
// trusted measured peak requirement, never installed RAM or model file size.
// The caller must stop/join the child before release. Cancellation retains the
// slot until cleanup. This is admission, not an OS process memory limit.
func (a *Agent) ReservePrivateRuntime(parent context.Context, memoryBytes uint64) (context.Context, func(), error) {
	if parent == nil || parent.Err() != nil || memoryBytes == 0 {
		return nil, nil, errors.New("valid context and measured runtime memory required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.admitLocked(true); err != nil {
		return nil, nil, err
	}
	if memoryBytes > a.cfg.MemoryLimitBytes || memoryBytes > a.telemetry.AvailableMemoryBytes-a.cfg.ReserveMemoryBytes {
		return nil, nil, errors.New("runtime exceeds approved memory headroom")
	}
	ctx, cancel := context.WithCancel(parent)
	a.runtimeMemory = memoryBytes
	a.cancel = cancel
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.mu.Lock()
				allowed := a.admitLocked(false) == nil
				a.mu.Unlock()
				if !allowed {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			<-done
			a.mu.Lock()
			a.runtimeMemory = 0
			a.cancel = nil
			a.wakeHeartbeatLocked()
			a.mu.Unlock()
		})
	}
	a.wakeHeartbeatLocked()
	return ctx, release, nil
}
