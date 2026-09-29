package main

import (
	"context"
	"errors"
	"time"

	"nexal/connector/internal/cybersecurity"
)

func canaryCommand(ctx context.Context, args []string) error {
	f, path, err := flags("canary")
	if err != nil {
		return err
	}
	action := f.String("action", "status", "status, enable, or disable")
	if err := parse(f, args, path); err != nil {
		return err
	}
	monitor := cybersecurity.CanaryForConfig(*path)
	switch *action {
	case "enable":
		err = monitor.Configure(true)
	case "disable":
		err = monitor.Configure(false)
	case "status":
	default:
		return errors.New("canary action must be status, enable, or disable")
	}
	if err != nil {
		return err
	}
	state, err := monitor.Status()
	if err != nil {
		return err
	}
	return emit(map[string]any{"enabled": state.Enabled, "status": state.Status, "lastCheckedAt": state.LastCheckedAt, "latestSignal": state.LastEvent, "description": "Watches only neXal's own decoy for changes or removal. Does not scan documents, detect reads, or automatically mitigate threats."})
}
func runCanary(ctx context.Context, monitor cybersecurity.Canary, report func(context.Context, cybersecurity.Event) error, reportState ...func(context.Context, cybersecurity.CanaryState) error) {
	var lastStatus string
	var lastReported time.Time
	for {
		request, stop := context.WithTimeout(ctx, 60*time.Second)
		tickErr := monitor.Tick(request, time.Now(), report)
		if len(reportState) > 0 {
			state, err := monitor.Status()
			if err != nil {
				state = cybersecurity.CanaryState{Enabled: true, Status: "error"}
			}
			if tickErr != nil && state.Enabled {
				state.Status = "error"
			}
			// Five-minute heartbeat, or the next one-minute local check on change.
			// Never transmit a baseline, marker or filesystem path.
			if state.Status != lastStatus || time.Since(lastReported) >= 5*time.Minute {
				if reportState[0](request, state) == nil {
					lastStatus = state.Status
					lastReported = time.Now()
				}
			}
		}
		stop()
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
