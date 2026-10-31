package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
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
//	nexal inference remote-offload <request-id> --mode off|large|always [--max-prompt N] [--max-wait S]
//	                                                      (how the Mac serving that request uses its GPU tier)
//	nexal inference remote-stats                          (AI throughput the Macs reported, as one JSON document)
//	nexal inference remote-test <endpoint> [--json]        (inference_run.go)
//
// The coordinator only queues the request. The other Mac's connector installs and serves the model with its
// own `nexal inference` commands, under its own owner policy, on its neXal address only.
func inferenceRemote(ctx context.Context, args []string) error {
	var to, pool, mode *string
	var wait, asJSON *bool
	var maxPrompt, maxWait *int
	needID := args[0] != "remote-list" && args[0] != "remote-stats"
	id, _, path, err := parseModelArgs(args[0], args[1:], needID, func(f *flag.FlagSet) {
		to = f.String("to", "", "host id of the Mac that should serve the model")
		pool = f.String("pool", "", "comma-separated host ids that serve the model together, the first one answering (instead of --to)")
		wait = f.Bool("wait", false, "wait until the model is being served or fails")
		asJSON = f.Bool("json", false, "print JSON")
		mode = f.String("mode", "", "remote-offload: off, large or always")
		maxPrompt = f.Int("max-prompt", 0, "remote-offload --mode large: prompts over this many tokens go to the GPU")
		maxWait = f.Int("max-wait", 0, "remote-offload --mode large: a local queue longer than this many seconds goes to the GPU")
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
	case "remote-offload":
		switch *mode {
		case "off", "always":
		case "large":
			if *maxPrompt <= 0 && *maxWait <= 0 {
				return &codedError{code: "invalid_arguments", err: errors.New("--mode large needs --max-prompt or --max-wait")}
			}
		default:
			return &codedError{code: "invalid_arguments", err: errors.New("--mode must be off, large or always")}
		}
		if *maxPrompt < 0 || *maxPrompt > 10_000_000 || *maxWait < 0 || *maxWait > 3600 {
			return &codedError{code: "invalid_arguments", err: errors.New("--max-prompt must be 0-10000000 and --max-wait 0-3600")}
		}
		p := remoteinference.OffloadPolicy{Mode: *mode, MaxPromptTokens: *maxPrompt, MaxWaitSeconds: *maxWait}
		if err := api.SetRemoteOffload(ctx, c.HostID, id, p); err != nil {
			return &codedError{code: "remote_failed", err: err}
		}
		_ = enc.Encode(map[string]any{"ok": true, "requestId": id, "mode": p.Mode})
	case "remote-stats":
		hosts, err := api.InferenceStats(ctx, c.HostID)
		if err != nil {
			return &codedError{code: "remote_failed", err: err}
		}
		if hosts == nil {
			hosts = []client.InferenceHostStats{}
		}
		_ = enc.Encode(map[string]any{"hosts": hosts})
	case "remote-stop":
		if err := api.CancelRemoteInference(ctx, c.HostID, id); err != nil {
			return &codedError{code: "remote_failed", err: err}
		}
	case "remote-serve":
		if *pool != "" {
			return servePool(ctx, api, c.HostID, id, strings.Split(*pool, ","), *wait, show)
		}
		if *to == "" {
			return &codedError{code: "invalid_arguments", err: errors.New("--to <host-id> (or --pool a,b) is required")}
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

// servePool asks several Macs to serve one model together (the first listed answers) and, with --wait,
// follows the pool until it answers or fails.
func servePool(ctx context.Context, api *client.Client, self, model string, hosts []string, wait bool, show func(remoteinference.Task)) error {
	tasks, err := api.CreateRemoteInferencePool(ctx, self, hosts, model)
	if err != nil {
		return &codedError{code: "remote_failed", err: err}
	}
	for _, t := range tasks {
		show(t)
	}
	for wait {
		lead := tasks[0]
		if lead.State == "ready" || lead.State == "failed" || lead.State == "cancelled" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		all, err := api.AllRemoteInference(ctx, self)
		if err != nil {
			return &codedError{code: "remote_failed", err: err}
		}
		for _, n := range all {
			for i, old := range tasks {
				if n.ID == old.ID && (n.State != old.State || n.Step != old.Step || n.Detail != old.Detail) {
					tasks[i] = n
					show(n)
				}
			}
		}
	}
	if tasks[0].State == "failed" {
		return &codedError{code: "remote_failed", err: errors.New(tasks[0].Detail)}
	}
	return nil
}
