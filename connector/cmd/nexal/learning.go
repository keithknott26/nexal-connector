package main

import (
	"context"
	"time"
)

// runLearning requests only coordinator-owned work. A connectivity watcher also
// cancels an in-flight request when mesh membership, consent, or capacity changes.
func runLearning(ctx context.Context, connected func() bool, tick func(context.Context) error) {
	runLearningWithInterval(ctx, connected, tick, 180*time.Second, time.Second)
}
func runLearningWithInterval(ctx context.Context, connected func() bool, tick func(context.Context) error, interval, watch time.Duration) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if connected() {
			callCtx, cancel := context.WithTimeout(ctx, 310*time.Second)
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(watch)
				defer ticker.Stop()
				for {
					select {
					case <-callCtx.Done():
						return
					case <-ticker.C:
						if !connected() {
							cancel()
							return
						}
					}
				}
			}()
			_ = tick(callCtx) // bounded retry below; never log evidence or provider errors
			cancel()
			<-done
		}
		timer.Reset(interval)
	}
}
