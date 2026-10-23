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
	"runtime"
	"strconv"
	"strings"

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

// addrList is a repeatable string flag (--also-listen A --also-listen B).
type addrList []string

func (a *addrList) String() string     { return strings.Join(*a, ",") }
func (a *addrList) Set(v string) error { *a = append(*a, v); return nil }

func inferenceServe(ctx context.Context, args []string) error {
	var host *string
	var port *int
	var also addrList
	id, _, path, err := parseModelArgs("serve", args, true, func(f *flag.FlagSet) {
		host = f.String("host", "127.0.0.1", "the IP address the model server binds (this Mac's neXal address to share with your other Macs)")
		port = f.Int("port", inference.DefaultServePort, "TCP port")
		f.Var(&also, "also-listen", "another IP address of this Mac to answer on, on the same port (repeatable): its neXal or LAN address")
	})
	if err != nil {
		return err
	}
	infDir := inferenceDirOf(*path)
	name, argv, env, serr := inference.ServeCommand(infDir, id, *host, *port)
	if serr != nil {
		return coded(serr)
	}
	extra, aerr := inference.ValidateAlsoListen(*host, also)
	if aerr != nil {
		return coded(aerr)
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
	// Each extra address relays to the server, so one server answers on loopback, the neXal network and the LAN.
	target := net.JoinHostPort(*host, strconv.Itoa(*port))
	for _, a := range extra {
		if _, err := inference.Relay(ctx, net.JoinHostPort(a, strconv.Itoa(*port)), target); err != nil {
			return &codedError{code: "port_in_use", err: fmt.Errorf("cannot answer on %s:%d: %w", a, *port, err)}
		}
	}
	writeLine(os.Stdout, "Serving %s on http://%s:%d/v1 (OpenAI-compatible; no sign-in, so only share it on a network you trust)", id, *host, *port)
	for _, a := range extra {
		writeLine(os.Stdout, "Also answering on http://%s/v1", net.JoinHostPort(a, strconv.Itoa(*port)))
	}
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return &codedError{code: "server_stopped", err: fmt.Errorf("the model server stopped: %w", err)}
	}
	return nil
}

// inferenceRemoteTest is `nexal inference remote-test <endpoint> [--json]`: it measures a model served by
// another Mac (reachability, time to first token, tokens per second) and checks it can answer a simple
// question. The endpoint must be a neXal mesh address or loopback.
func inferenceRemoteTest(ctx context.Context, args []string) error {
	var asJSON *bool
	var calls *int
	endpoint, _, _, err := parseModelArgs("remote-test", args, true, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print the result as one JSON document")
		calls = f.Int("calls", 0, "measure generation speed over this many calls in a row (2-60) instead of one test")
	})
	if err != nil {
		return err
	}
	if *calls != 0 {
		sw, err := inference.RemoteSweep(ctx, endpoint, *calls, nil, nil)
		if err != nil {
			return &codedError{code: "remote_test_failed", err: err}
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(sw)
		}
		writeLine(os.Stdout, "%s (%s)", sw.Endpoint, sw.Model)
		for _, x := range sw.Samples {
			if !x.OK {
				writeLine(os.Stdout, "  %2d  failed: %s", x.Call, x.Error)
				continue
			}
			writeLine(os.Stdout, "  %2d  %6.1f tokens/s  %s", x.Call, x.TokensPerSec, strings.Repeat("#", int(x.TokensPerSec/2)))
		}
		return nil
	}
	res, err := inference.RemoteTest(ctx, endpoint, nil)
	if err != nil {
		return &codedError{code: "remote_test_failed", err: err}
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(res)
	}
	writeLine(os.Stdout, "%s (%s)", res.Endpoint, res.Model)
	writeLine(os.Stdout, "  reachable:    %.0f ms", res.ReachableMs)
	writeLine(os.Stdout, "  first token:  %.0f ms", res.FirstTokenMs)
	writeLine(os.Stdout, "  speed:        %.1f tokens/s (%d tokens in %.1f s)", res.TokensPerSec, res.Tokens, res.TotalMs/1000)
	ok := "no - the model did not answer 17 + 25 correctly"
	if res.CapabilityOK {
		ok = "yes - answered 17 + 25 correctly"
	}
	writeLine(os.Stdout, "  capability:   %s", ok)
	return nil
}
