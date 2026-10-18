package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	// Rank 0's server listens on loopback and is forwarded to the neXal address below: this Mac cannot
	// connect to its own neXal address, so it could not otherwise tell that the server answers.
	listenHost := *apiHost
	if *rank == 0 {
		listenHost = "127.0.0.1"
	}
	name, argv, env, serr := inference.PoolServeCommand(infDir, id, *rank, strings.Split(*hosts, ","), listenHost, *apiPort, hostfile)
	if serr != nil {
		return coded(serr)
	}
	defer os.Remove(hostfile)
	if left := inference.ReapOrphanPoolServers(infDir); len(left) > 0 {
		writeLine(os.Stdout, "Stopped %d pool server(s) left over from an earlier run", len(left))
	}
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
	if err := cmd.Start(); err != nil {
		return &codedError{code: "server_stopped", err: fmt.Errorf("the pool server did not start: %w", err)}
	}
	if *rank == 0 {
		fctx, stop := context.WithCancel(ctx)
		defer stop()
		if err := inference.ForwardAPI(fctx, net.JoinHostPort(*apiHost, strconv.Itoa(*apiPort)), net.JoinHostPort(listenHost, strconv.Itoa(*apiPort))); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return &codedError{code: "server_stopped", err: fmt.Errorf("could not answer on %s:%d: %w", *apiHost, *apiPort, err)}
		}
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return &codedError{code: "server_stopped", err: fmt.Errorf("the pool server stopped: %w", err)}
	}
	return nil
}

// inferenceReviewed is `nexal inference reviewed [--json]`: the models this build can install with one
// click, because a reviewed commit and file hashes ship with it (or the owner pinned them).
func inferenceReviewed(ctx context.Context, args []string) error {
	var asJSON *bool
	_, _, path, err := parseModelArgs("reviewed", args, false, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print one JSON document")
	})
	if err != nil {
		return err
	}
	c, err := inference.LoadCatalogWithPins(inferenceDirOf(*path))
	if err != nil {
		return coded(err)
	}
	models := inference.ReviewedModels(c)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"schemaVersion": 1, "models": models})
	}
	if len(models) == 0 {
		writeLine(os.Stdout, "No model has been reviewed for this version yet.")
	}
	for _, m := range models {
		writeLine(os.Stdout, "%s  %s  %s", m.ID, m.Revision, m.DisplayName)
	}
	return nil
}
