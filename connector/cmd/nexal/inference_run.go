package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"nexal/connector/internal/inference"
)

// "Simple mode" one-click runtime:
//
//	nexal inference setup [--json]
//	nexal inference serve <model-id> [--host IP] [--port N]
//
// setup creates a private Python environment with the pinned MLX packages (pip, binary wheels only).
// serve verifies the installed model's files and runs mlx_lm's OpenAI-compatible server on one specific
// address (never all interfaces) until it is stopped. Neither starts anything on its own.

func inferenceSetup(ctx context.Context, args []string) error {
	var asJSON *bool
	_, _, path, err := parseModelArgs("setup", args, false, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print newline-delimited JSON events")
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	s := &inference.RuntimeSetup{
		InferenceDir: inferenceDirOf(*path), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Emit: func(ev any) {
			if *asJSON {
				_ = enc.Encode(ev)
				return
			}
			if e, ok := ev.(inference.SetupEvent); ok && e.Message != "" {
				writeLine(os.Stdout, "%s", e.Message)
			}
		},
	}
	if err := s.Setup(ctx); err != nil {
		return coded(err)
	}
	if !*asJSON {
		writeLine(os.Stdout, "The AI runtime is ready.")
	}
	return nil
}

func inferenceServe(ctx context.Context, args []string) error {
	var host *string
	var port *int
	id, _, path, err := parseModelArgs("serve", args, true, func(f *flag.FlagSet) {
		host = f.String("host", "127.0.0.1", "the one IP address to listen on (this Mac's neXal address to share with your other Macs)")
		port = f.Int("port", inference.DefaultServePort, "TCP port")
	})
	if err != nil {
		return err
	}
	infDir := inferenceDirOf(*path)
	name, argv, env, serr := inference.ServeCommand(infDir, id, *host, *port)
	if serr != nil {
		return coded(serr)
	}
	// Refuse damaged files before anything reads them.
	res, verr := inference.CheckModel(ctx, infDir, id, false)
	if verr != nil {
		return coded(verr)
	}
	if !res.OK {
		return &codedError{code: "model_damaged", err: errors.New("the model files changed since install; run `nexal inference verify " + id + "` and install it again")}
	}
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	writeLine(os.Stdout, "Serving %s on http://%s:%d/v1 (OpenAI-compatible; no sign-in, so only share it on a network you trust)", id, *host, *port)
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return &codedError{code: "server_stopped", err: fmt.Errorf("the model server stopped: %w", err)}
	}
	return nil
}
