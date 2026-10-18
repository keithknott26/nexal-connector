package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/remoteinference"
)

// Ask another Mac on this neXal network to serve an AI model:
//
//	nexal inference remote-serve <model-id> --to <host-id> [--wait] [--json]
//	nexal inference remote-list [--json]
//	nexal inference remote-stop <request-id>
//	nexal inference remote-test <endpoint> [--json]        (inference_run.go)
//
// The coordinator only queues the request. The other Mac's connector installs and serves the model with its
// own `nexal inference` commands, under its own owner policy, on its neXal address only.
func inferenceRemote(ctx context.Context, args []string) error {
	var to *string
	var wait, asJSON *bool
	needID := args[0] != "remote-list"
	id, _, path, err := parseModelArgs(args[0], args[1:], needID, func(f *flag.FlagSet) {
		to = f.String("to", "", "host id of the Mac that should serve the model")
		wait = f.Bool("wait", false, "wait until the model is being served or fails")
		asJSON = f.Bool("json", false, "print JSON")
	})
	if err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if c.HostID == "" {
		return &codedError{code: "not_enrolled", err: errors.New("this Mac is not enrolled")}
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return err
	}
	api, err := client.New(c.Coordinator, token, c.Development)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	show := func(t remoteinference.Task) {
		if *asJSON {
			_ = enc.Encode(t)
			return
		}
		writeLine(os.Stdout, "%s  %s on %s: %s %s %s %s", t.ID, t.ModelID, t.TargetHostID, t.State, t.Step, t.Detail, t.Endpoint)
	}
	switch args[0] {
	case "remote-list":
		tasks, err := api.AllRemoteInference(ctx, c.HostID)
		if err != nil {
			return &codedError{code: "remote_failed", err: err}
		}
		for _, t := range tasks {
			show(t)
		}
	case "remote-stop":
		if err := api.CancelRemoteInference(ctx, c.HostID, id); err != nil {
			return &codedError{code: "remote_failed", err: err}
		}
	case "remote-serve":
		if *to == "" {
			return &codedError{code: "invalid_arguments", err: errors.New("--to <host-id> is required")}
		}
		t, err := api.CreateRemoteInference(ctx, c.HostID, *to, id)
		if err != nil {
			return &codedError{code: "remote_failed", err: err}
		}
		show(*t)
		for *wait && t.State != "ready" && t.State != "failed" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			all, err := api.AllRemoteInference(ctx, c.HostID)
			if err != nil {
				return &codedError{code: "remote_failed", err: err}
			}
			for _, n := range all {
				if n.ID == t.ID && (n.State != t.State || n.Step != t.Step || n.Detail != t.Detail) {
					*t = n
					show(n)
				}
			}
		}
		if t.State == "failed" {
			return &codedError{code: "remote_failed", err: errors.New(t.Detail)}
		}
	}
	return nil
}
