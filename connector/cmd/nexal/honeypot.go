package main

import (
	"context"
	"errors"

	"nexal/connector/internal/cybersecurity"
)

func honeypotCommand(ctx context.Context, args []string) error {
	f, path, err := flags("honeypot")
	if err != nil {
		return err
	}
	action := f.String("action", "status", "status, enable, or disable")
	if err := parse(f, args, path); err != nil {
		return err
	}
	honeypot := cybersecurity.HoneypotForConfig(*path)
	switch *action {
	case "enable":
		err = honeypot.Configure(true)
	case "disable":
		err = honeypot.Configure(false)
	case "status":
	default:
		return errors.New("honeypot action must be status, enable, or disable")
	}
	if err != nil {
		return err
	}
	state, err := honeypot.Status()
	if err != nil {
		return err
	}
	recent := state.Recent
	if recent == nil {
		recent = []cybersecurity.HoneypotConnection{}
	}
	return emit(map[string]any{"enabled": state.Enabled, "status": state.Status, "ports": state.Ports, "triggers": state.Triggers,
		"lastTriggeredAt": state.LastTriggeredAt, "pendingEvents": len(state.Pending), "suppressed": state.Suppressed, "recent": recent,
		"description": "Opens fake services on this Mac. Nothing legitimate should ever connect, so any connection from another computer is reported as an alert. It never runs commands or accepts logins."})
}
