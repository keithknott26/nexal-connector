package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"nexal/connector/internal/agent"
	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/diagnostics"
	"nexal/connector/internal/discovery"
	"nexal/connector/internal/tunnel"
)

func emit(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": map[string]string{"code": "connector_error", "message": err.Error()}})
		os.Exit(1)
	}
}
func flags(command string) (*flag.FlagSet, *string, error) {
	defaultPath, err := config.DefaultPath()
	if err != nil {
		return nil, nil, errors.New("cannot resolve configuration directory")
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard) // never echo accidentally supplied secret arguments
	path := f.String("config", defaultPath, "absolute configuration path")
	return f, path, nil
}
func parse(f *flag.FlagSet, args []string, path *string) error {
	if err := f.Parse(args); err != nil {
		return errors.New("invalid command flags; consult connector/CLI-CONTRACT.md")
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !filepath.IsAbs(*path) {
		return errors.New("--config must be an absolute path")
	}
	return nil
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nexal init|enroll|coordinator-check|run|status|policy|set-policy|pause|resume|accept-jobs|cancel|doctor|tunnel-check|bundle-send|bundle-receive [--config absolute-path]")
	}
	switch args[0] {
	case "coordinator-check":
		return coordinatorCheckCommand(ctx, args[1:])
	case "bundle-send", "bundle-receive":
		return bundleCommand(ctx, args[0], args[1:])
	case "init":
		return initCommand(ctx, args[1:])
	case "enroll":
		return enrollCommand(ctx, args[1:])
	case "run":
		return runCommand(ctx, args[1:])
	case "status", "policy", "pause", "resume", "accept-jobs", "cancel":
		return localCommand(ctx, args[0], args[1:])
	case "set-policy":
		return policyCommand(ctx, args[1:])
	case "tunnel-check":
		return tunnelCommand(ctx, args[1:])
	case "doctor":
		return doctorCommand(ctx, args[1:])
	case "self-test":
		return selfTestCommand(ctx, args[1:])
	case "version":
		return emit(map[string]string{"version": config.Version})
	default:
		return errors.New("unknown command")
	}
}
func doctorCommand(ctx context.Context, args []string) error {
	f, path, err := flags("doctor")
	if err != nil {
		return err
	}
	probe := f.Bool("probe", false, "explicitly run bounded local Mac telemetry checks")
	if err := parse(f, args, path); err != nil {
		return err
	}
	return emit(diagnostics.Inspect(ctx, *path, *probe))
}
func initCommand(ctx context.Context, args []string) error {
	f, path, err := flags("init")
	if err != nil {
		return err
	}
	base := f.String("coordinator", "", "HTTPS coordinator origin")
	name := f.String("name", "Nexal Mac", "owner-readable host name")
	listen := f.String("listen", "127.0.0.1:8788", "numeric loopback address")
	dev := f.Bool("dev-loopback", false, "explicit nonproduction loopback test mode")
	devSecrets := f.Bool("dev-secrets", false, "nonproduction 0600 credentials")
	memory := f.Uint64("memory-limit-mib", 256, "approved workload memory cap")
	reserve := f.Uint64("reserve-memory-mib", 1024, "reserved owner memory")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if *memory > 8192 || *reserve > 1<<20 {
		return errors.New("memory limits are outside safe bounds")
	}
	if _, err := os.Lstat(*path); err == nil {
		return errors.New("configuration already exists; initialization never overwrites credentials")
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect configuration")
	}
	c := config.Config{Version: 1, Coordinator: strings.TrimRight(*base, "/"), Name: *name, Listen: *listen,
		Development: *dev, DevSecrets: *devSecrets, Paused: true, MemoryLimitBytes: *memory << 20, ReserveMemoryBytes: *reserve << 20, IdleSeconds: 300}
	if err := c.Validate(); err != nil {
		return err
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	token, err := config.RandomToken()
	if err != nil {
		return errors.New("cannot generate admin credential")
	}
	if err = secrets.Put(ctx, "admin", token); err != nil {
		return err
	}
	if err = config.Save(*path, c); err != nil {
		return err
	}
	mode := "production — job dispatch gated"
	if c.Development {
		mode = "DEVELOPMENT PREVIEW — no public work or external spending"
	}
	return emit(map[string]any{"initialized": true, "paused": true, "marketplaceEnabled": false, "mode": mode})
}
func enrollCommand(ctx context.Context, args []string) error {
	f, path, err := flags("enroll")
	if err != nil {
		return err
	}
	stdin := f.Bool("code-stdin", false, "read enrollment code from stdin")
	synthetic := f.Bool("dev-synthetic-hardware", false, "explicit development 8 GiB simulated inventory")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if !*stdin {
		return errors.New("--code-stdin is required; enrollment secrets must not be placed in command arguments")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *synthetic && !c.Development {
		return errors.New("synthetic hardware is forbidden in production")
	}
	unlock, err := config.Lock(*path)
	if err != nil {
		return err
	}
	defer unlock()
	if c.HostID != "" {
		return errors.New("host already enrolled; use a new configuration for a new host identity")
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 257))
	if err != nil || len(b) > 256 {
		return errors.New("enrollment code input exceeds limit")
	}
	code := strings.TrimSpace(string(b))
	if len(code) < 6 || strings.ContainsAny(code, " \t\r\n") {
		return errors.New("invalid enrollment code")
	}
	api, err := client.New(c.Coordinator, "", c.Development)
	if err != nil {
		return err
	}
	telemetry := agent.MacProbe(c.IdleSeconds)(ctx)
	if *synthetic {
		telemetry.TotalMemoryBytes = 8 << 30
	}
	id, err := api.Enroll(ctx, client.Enrollment{Code: code, Name: c.Name, Platform: runtime.GOOS, Arch: runtime.GOARCH,
		CPUCores: runtime.NumCPU(), MemoryBytes: telemetry.TotalMemoryBytes, StorageBytes: 0})
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	if err = secrets.Put(ctx, "host", id.Token); err != nil {
		return err
	}
	c.HostID = id.HostID
	if err = config.Save(*path, c); err != nil {
		return err
	}
	return emit(map[string]any{"enrolled": true, "hostId": c.HostID, "marketplaceEnabled": false})
}
func runCommand(ctx context.Context, args []string) error {
	f, path, err := flags("run")
	if err != nil {
		return err
	}
	pull := f.Bool("dev-private-pull", false, "development-only outbound private CPU execution")
	idle := f.Bool("dev-assume-idle", false, "development-only synthetic idle/memory telemetry")
	if err := parse(f, args, path); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if (*pull || *idle) && !c.Development {
		return errors.New("development flags are forbidden for production configuration")
	}
	if !client.ValidID(c.HostID) {
		return errors.New("enroll this host before running")
	}
	unlock, err := config.Lock(*path)
	if err != nil {
		return err
	}
	defer unlock()
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	admin, err := secrets.Get(ctx, "admin")
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
	// Agent.Refresh applies the current mutable idle threshold.
	probe := agent.MacProbe(0)
	if *idle {
		probe = func(context.Context) agent.Telemetry {
			return agent.Telemetry{Known: true, Synthetic: true, IdleSeconds: 86400,
				TotalMemoryBytes: 8 << 30, AvailableMemoryBytes: 4 << 30}
		}
	}
	// Structured logs go to stderr as JSON: stdout carries the CLI's JSON command
	// output (see emit and CLI-CONTRACT.md), so logging there would corrupt it.
	// Records never include tokens, ciphertext or coordinator URLs.
	// Development configurations get the high-frequency records (per-poll and
	// per-retry failures); production keeps to attempt-level and fault records.
	level := slog.LevelInfo
	if c.Development {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	opts := []agent.Option{agent.WithLogger(logger)}
	if c.Discovery.Enabled() {
		// Off unless the owner turned it on: config.Discovery is absent by default,
		// and Validate has already refused a gate opened without a fingerprint and
		// port. ResourceSharing stays false here — §36.4 is unresolved, and nothing
		// in this wiring offers this machine's CPU, GPU or RAM to anyone.
		//
		// Discovery grants nothing. Advertise publishes a candidate list entry, the
		// directory read produces candidates, and admission stays with the
		// mutual-TLS peerPolicy in internal/pool.
		opts = append(opts, agent.WithDiscovery(agent.DiscoveryOptions{
			Config: c.Discovery,
			Sharing: discovery.SharingPolicy{
				LANDiscovery:  c.Discovery.LANDiscovery,
				WANRendezvous: c.Discovery.WANRendezvous,
			},
			Publisher: api, Source: api,
		}))
	}
	a, err := agent.New(c, *path, api, probe, *pull, opts...)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); results <- a.Serve(ctx, admin) }()
	go func() { defer wg.Done(); results <- a.Run(ctx) }()
	if c.Tunnel != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- tunnel.Run(ctx, *c.Tunnel, c.Listen, filepath.Dir(*path), func(e tunnel.Evidence) {
				a.SetPQ(client.PQ{Configured: e.Configured && !e.Quarantined, Protocol: e.ObservedProtocol})
				// Persist sanitized evidence, not tokens or raw cloudflared logs.
				b, _ := json.MarshalIndent(e, "", "  ")
				if config.AtomicPrivate(filepath.Join(filepath.Dir(*path), "tunnel-evidence.json"), b) != nil || e.Quarantined {
					a.Cancel()
				}
			})
		}()
	}
	select {
	case err = <-results:
	case <-ctx.Done():
	}
	cancel()
	wg.Wait()
	return err
}
func localCommand(ctx context.Context, command string, args []string) error {
	f, path, err := flags(command)
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	method := "POST"
	if command == "status" || command == "policy" {
		method = "GET"
	}
	return localRequest(ctx, *path, method, command, nil)
}

func policyCommand(ctx context.Context, args []string) error {
	f, path, err := flags("set-policy")
	if err != nil {
		return err
	}
	memory := f.Uint64("memory-limit-mib", 0, "workload cap, 64–8192 MiB")
	reserve := f.Uint64("reserve-memory-mib", 0, "owner reserve, 128–1048576 MiB")
	idle := f.Uint64("idle-seconds", 0, "idle threshold, 30–86400 seconds")
	if err := parse(f, args, path); err != nil {
		return err
	}
	// Require all fields; never accidentally reset an omitted consent limit.
	if *memory > 8192 || *reserve > 1<<20 {
		return errors.New("memory limits are outside safe bounds")
	}
	policy := config.ResourcePolicy{MemoryLimitBytes: *memory << 20,
		ReserveMemoryBytes: *reserve << 20, IdleSeconds: *idle}
	if err := policy.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(policy)
	if err != nil {
		return errors.New("cannot encode resource policy")
	}
	return localRequest(ctx, *path, "PUT", "policy", body)
}

func localRequest(ctx context.Context, path, method, command string, body []byte) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(path, c)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "admin")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.Listen+"/v1/"+command, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot construct local command")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil,
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local redirect forbidden") }}
	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.New("connector is not running or local API is unavailable")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return errors.New("invalid local API response")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("local command rejected (HTTP %d)", resp.StatusCode)
	}
	var out any
	if json.NewDecoder(bytes.NewReader(b)).Decode(&out) != nil {
		return errors.New("invalid local API JSON")
	}
	return emit(out)
}
func tunnelCommand(ctx context.Context, args []string) error {
	f, path, err := flags("tunnel-check")
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if c.Tunnel == nil {
		return errors.New("no pinned cloudflared configuration; incoming PQ tunnel is not configured")
	}
	e, err := tunnel.Check(ctx, *c.Tunnel, c.Listen)
	if err != nil {
		return err
	}
	return emit(e)
}

// self-test is an explicitly owner-initiated, offline constant-memory smoke
// test, not dispatch, an execution grant, paid work or a public service.
func selfTestCommand(ctx context.Context, args []string) error {
	f, path, err := flags("self-test")
	if err != nil {
		return err
	}
	samples := f.Int64("samples", 1_000_000, "local fixed CPU self-test samples (1–1000000)")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if *samples < 1 || *samples > 1_000_000 {
		return errors.New("self-test samples must be 1–1000000")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	unlock, err := config.Lock(*path)
	if err != nil {
		return err
	}
	defer unlock()
	// Owner activity is expected for an explicitly requested offline self-test,
	// but real macOS memory telemetry is still required in production.
	telemetry := agent.MacProbe(c.IdleSeconds)(ctx)
	if !c.Development && (!telemetry.Known || telemetry.AvailableMemoryBytes < c.ReserveMemoryBytes+agent.WorkloadMemoryBytes) {
		return errors.New("self-test requires known owner-reserved memory headroom")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	result, err := agent.MonteCarlo(ctx, client.Attempt{ID: "owner-local-self-test", Template: agent.Template, Samples: *samples},
		func() time.Time { return deadline })
	if err != nil {
		return errors.New("self-test cancelled or exceeded time limit")
	}
	return emit(map[string]any{"mode": "owner-initiated offline self-test", "template": agent.Template, "result": result,
		"networkUsed": false, "marketplaceEnabled": false, "ledgerEffects": false})
}
