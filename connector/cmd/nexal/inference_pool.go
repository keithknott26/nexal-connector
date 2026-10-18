package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"nexal/connector/internal/inference"
)

// Pooling one model across several Macs:
//
//	nexal inference pool-setup [--json]
//	nexal inference pool-serve <model-id> --rank N --hosts ip:port,ip:port --api-host IP [--api-port N]
//
// pool-setup builds the separate environment pooling needs. pool-serve runs this Mac's share of the
// pool; run it with the same --hosts on every member, each with its own rank. Rank 0 answers the
// OpenAI-compatible API; the other ranks work for it until it stops.

func inferencePoolSetup(ctx context.Context, args []string) error {
	var asJSON *bool
	_, _, path, err := parseModelArgs("pool-setup", args, false, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print newline-delimited JSON events")
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	s := &inference.RuntimeSetup{
		Pool: true, InferenceDir: inferenceDirOf(*path), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
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
		writeLine(os.Stdout, "The pooling runtime is ready.")
	}
	return nil
}

func inferencePoolServe(ctx context.Context, args []string) error {
	var rank, apiPort *int
	var hosts, apiHost *string
	id, _, path, err := parseModelArgs("pool-serve", args, true, func(f *flag.FlagSet) {
		rank = f.Int("rank", -1, "this Mac's position in --hosts (0 answers the API)")
		hosts = f.String("hosts", "", "every member's ring address, comma separated, in rank order (ip:port)")
		apiHost = f.String("api-host", "", "the one IP address rank 0 answers on (its neXal address)")
		apiPort = f.Int("api-port", inference.DefaultServePort, "TCP port rank 0 answers on")
	})
	if err != nil {
		return err
	}
	infDir := inferenceDirOf(*path)
	hostfile := filepath.Join(infDir, fmt.Sprintf("pool-%s-rank%d.json", id, *rank))
	name, argv, env, serr := inference.PoolServeCommand(infDir, id, *rank, strings.Split(*hosts, ","), *apiHost, *apiPort, hostfile)
	if serr != nil {
		return coded(serr)
	}
	defer os.Remove(hostfile)
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
	if *rank == 0 {
		writeLine(os.Stdout, "Pool rank 0 serving %s on http://%s:%d/v1", id, *apiHost, *apiPort)
	} else {
		writeLine(os.Stdout, "Pool rank %d working for rank 0 on %s", *rank, id)
	}
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return &codedError{code: "server_stopped", err: fmt.Errorf("the pool server stopped: %w", err)}
	}
	return nil
}
