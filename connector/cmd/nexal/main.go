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
	"nexal/connector/internal/cybersecurity"
	"nexal/connector/internal/diaglog"
	"nexal/connector/internal/diagnostics"
	"nexal/connector/internal/discovery"
	"nexal/connector/internal/mesh"
	"nexal/connector/internal/observability"
	"nexal/connector/internal/presence"
	"nexal/connector/internal/privateruntime"
	"nexal/connector/internal/sysinfo"
	"nexal/connector/internal/tunnel"
	"nexal/connector/internal/wol"
)

func emit(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdown, telemetryErr := observability.Start(ctx)
	if telemetryErr != nil {
		slog.Warn("telemetry disabled: invalid or unavailable collector configuration")
	}
	runErr := run(ctx, os.Args[1:])
	if shutdown != nil {
		flushCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_ = shutdown(flushCtx)
		cancel()
	}
	if err := runErr; err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": map[string]string{"code": errorCode(err), "message": err.Error()}})
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
		return errors.New("usage: nexal init|enroll|identity|coordinator-check|run|status|peers-view|time-machine|policy|set-policy|pause|resume|accept-jobs|cancel|pair|pair-v2|doctor|tunnel-check|static-peers|peers|collective|drive|share|lan-share|wake|canary|honeypot|sandbox|peer-names|share-while-active|security-import|diagnostics|bundle-send|bundle-receive [--config absolute-path]")
	}
	switch args[0] {
	case "exit-route":
		return exitRouteCommand(ctx, args[1:])
	case "guest":
		return guestCommand(ctx, args[1:])
	case "honeypot":
		return honeypotCommand(ctx, args[1:])
	case "sandbox":
		return sandboxCommand(ctx, args[1:])
	case "peer-names":
		return peerNamesCommand(ctx, args[1:])
	case "canary":
		return canaryCommand(ctx, args[1:])
	case "security-import":
		return securityImportCommand(ctx, args[1:])
	case "coordinator-check":
		return coordinatorCheckCommand(ctx, args[1:])
	case "diagnostics":
		return diagnosticsCommand(ctx, args[1:])
	case "bundle-send", "bundle-receive":
		return bundleCommand(ctx, args[0], args[1:])
	case "init":
		return initCommand(ctx, args[1:])
	case "enroll":
		return enrollCommand(ctx, args[1:])
	case "identity":
		return identityCommand(ctx, args[1:])
	case "run":
		return runCommand(ctx, args[1:])
	case "status", "policy", "peers-view", "pause", "resume", "accept-jobs", "cancel":
		return localCommand(ctx, args[0], args[1:])
	case "set-policy":
		return policyCommand(ctx, args[1:])
	case "share-while-active":
		// nexal share-while-active on|off: take work even while the owner is using this Mac.
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			return errors.New("usage: nexal share-while-active on|off [--config absolute-path]")
		}
		return localCommand(ctx, "share-while-active-"+args[1], args[2:])
	case "tunnel-check":
		return tunnelCommand(ctx, args[1:])
	case "doctor":
		return doctorCommand(ctx, args[1:])
	case "static-peers":
		return staticPeersCommand(ctx, args[1:])
	case "peers":
		return peersCommand(ctx, args[1:])
	case "share":
		return shareCommand(ctx, args[1:])
	case "lan-share":
		return lanShareCommand(ctx, args[1:])
	case "drive":
		return driveCommand(ctx, args[1:])
	case "collective":
		return collectiveCommand(ctx, args[1:])
	case "pair":
		return pairCommand(ctx, args[1:])
	case "pair-v2":
		return pairV2Command(ctx, args[1:])
	case "wake":
		return wakeCommand(ctx, args[1:])
	case "time-machine":
		return timeMachineCommand(ctx, args[1:])
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
	// Opt-in for the same reason --probe is: it is the only part of doctor that
	// leaves the machine. It sends STUN binding requests, which carry no
	// credential and no host identity, and it establishes nothing.
	nat := f.Bool("stun", false, "explicitly query public STUN servers for this host's reflexive address and NAT mapping class")
	if err := parse(f, args, path); err != nil {
		return err
	}
	return emit(diagnostics.Inspect(ctx, *path, *probe, *nat))
}

// staticPeersCommand lists, adds and removes owner-configured cross-VLAN peer
// endpoints.
//
// It edits the configuration FILE under the same exclusive lock enroll and
// self-test use, rather than going through the running agent's local API like
// set-policy. That is a deliberate difference: a resource limit must take effect
// on a live host immediately, whereas a peer endpoint is read when discovery
// starts, so pretending a live update happened would be the dishonest option. The
// output says a restart is required.
//
// A configured endpoint is NOT authorization (HARDENING-PLAN §30.2). The
// fingerprint is mandatory because pool.PeerOptions pins one exact device
// fingerprint per dial, and the coordinator's authorized set still decides
// AllowedPeers — adding a peer here cannot widen it.
func staticPeersCommand(_ context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nexal static-peers list|add|remove [--endpoint https://10.20.0.5:8443] [--fingerprint 64-hex] [--label text]")
	}
	action := args[0]
	f, path, err := flags("static-peers " + action)
	if err != nil {
		return err
	}
	endpoint := f.String("endpoint", "", "https URL with a literal private IP and explicit port")
	fingerprint := f.String("fingerprint", "", "expected peer device fingerprint, 64 lowercase hex")
	label := f.String("label", "", "optional owner-readable note")
	if err := parse(f, args[1:], path); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if action == "list" {
		if *endpoint != "" || *fingerprint != "" || *label != "" {
			return errors.New("static-peers list takes no peer flags")
		}
		peers := c.StaticPeers
		if peers == nil {
			peers = []config.StaticPeer{}
		}
		return emit(map[string]any{"staticPeers": peers, "count": len(peers),
			"authorization": "configuration is not authorization: a static peer is dialable only while the coordinator lists its fingerprint",
			"discovery":     "static peers exist because mDNS is link-local and does not cross a VLAN; the transport itself already permits a routed private address"})
	}
	// Mutations take the exclusive config lock so a concurrent run/enroll cannot
	// interleave a write, and are validated before anything is persisted.
	unlock, err := config.Lock(*path)
	if err != nil {
		return err
	}
	defer unlock()
	// Re-read under the lock: the copy above was read without it.
	if c, err = config.Load(*path); err != nil {
		return err
	}
	switch action {
	case "add":
		// Encode the flags and decode them back through the strict exactly-once
		// decoder, so CLI input and a stored/PUT payload share ONE validation path
		// instead of two that can drift.
		fields := map[string]string{"endpoint": *endpoint, "fingerprint": *fingerprint}
		if *label != "" {
			fields["label"] = *label
		}
		body, err := json.Marshal(fields)
		if err != nil {
			return errors.New("cannot encode static peer")
		}
		peer, err := config.DecodeStaticPeer(body)
		if err != nil {
			return err
		}
		updated, err := config.AddStaticPeer(c.StaticPeers, peer)
		if err != nil {
			return err
		}
		c.StaticPeers = updated
	case "remove":
		if *endpoint != "" {
			// Removal is by fingerprint because that is the stable identity; an
			// address changes, and removing by address would strand a pin.
			return errors.New("remove takes --fingerprint, not --endpoint")
		}
		updated, err := config.RemoveStaticPeer(c.StaticPeers, *fingerprint)
		if err != nil {
			return err
		}
		c.StaticPeers = updated
	default:
		return errors.New("unknown static-peers action; use list, add or remove")
	}
	if err := config.Save(*path, c); err != nil {
		return err
	}
	return emit(map[string]any{"staticPeers": c.StaticPeers, "count": len(c.StaticPeers),
		"appliesAt":     "next nexal run; a running agent keeps the peer list it started with",
		"authorization": "configuration is not authorization: the coordinator's authorized set still decides which fingerprints may be dialed"})
}
func initCommand(ctx context.Context, args []string) error {
	f, path, err := flags("init")
	if err != nil {
		return err
	}
	base := f.String("coordinator", "", "HTTPS coordinator origin")
	name := f.String("name", "neXal Mac", "owner-readable host name")
	listen := f.String("listen", "127.0.0.1:8788", "numeric loopback address")
	dev := f.Bool("dev-loopback", false, "explicit nonproduction loopback test mode")
	devSecrets := f.Bool("dev-secrets", false, "nonproduction 0600 credentials")
	memory := f.Uint64("memory-limit-mib", 256, "approved workload memory cap")
	reserve := f.Uint64("reserve-memory-mib", 1024, "reserved owner memory")
	// Default false. Contributing resources is the point of joining, so the opt-out is
	// a flag rather than the starting state.
	paused := f.Bool("paused", false, "join without contributing resources until resumed")
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
	// Paused defaults to FALSE: sharing resources is the purpose of joining, so a new
	// configuration that starts paused makes the owner turn on the one thing they just
	// opted into. The resource and idle policies still gate what actually runs -- this
	// changes the starting position, not the limits.
	//
	// --paused remains available for a machine that should join without contributing.
	c := config.Config{Version: 1, Coordinator: strings.TrimRight(*base, "/"), Name: *name, Listen: *listen,
		Development: *dev, DevSecrets: *devSecrets, Paused: *paused, MemoryLimitBytes: *memory << 20, ReserveMemoryBytes: *reserve << 20, IdleSeconds: 300,
		ShareWhileActive: true}
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
	// Reports the value actually written. This was hardcoded true, which was merely
	// redundant while Paused was always true and would now be false reporting.
	return emit(map[string]any{"initialized": true, "paused": *paused, "marketplaceEnabled": false, "mode": mode})
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
	return runCommandWithMachineLock(ctx, args, config.LockMachine)
}

// The test seam keeps integration tests away from a real running connector's lock.
// Production always supplies the host-wide lock above; no environment bypass exists.
func runCommandWithMachineLock(ctx context.Context, args []string, lockMachine func(string) (func(), error)) error {
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
	// Host-wide exclusion, on top of the per-configuration lock above.
	//
	// config.Lock only stops a second connector on the SAME configuration, so the
	// macOS app's development profile in "Application Support/Nexal-Development" took
	// its own lock and ran alongside production: two agents heartbeating, advertising
	// addresses and answering as one host.
	//
	// Taken by `run` alone, and deliberately not by enroll/peers/share/self-test. Those
	// are short-lived and frequently used WHILE the agent runs; a host-wide lock in
	// config.Lock itself would make `nexal peers` fail whenever the agent is up.
	unlockHost, err := lockMachine(*path)
	if err != nil {
		return err
	}
	defer unlockHost()
	if c.GuestAccess != nil {
		if c.GuestAccess.IsExpired(time.Now()) {
			_ = expireGuest(ctx, *path, &c)
			return guestExpiredError(c.GuestAccess)
		}
		if !guestGuardReady(ctx, *path, *c.GuestAccess) {
			_ = (mesh.ExecRunner{}).Run(ctx, "nexal-network", "down")
			return errors.New("temporary access requires its installed expiry guard")
		}
	}
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
	//
	// Diagnostic mode (`nexal diagnostics --on`, or the Mac app's Diagnostics
	// toggle) raises this level to Debug while the agent runs, without a restart,
	// and mirrors every record into the size-capped diagnostics.log. The agent
	// re-reads the flag file every few seconds.
	level := slog.LevelInfo
	if c.Development {
		level = slog.LevelDebug
	}
	logDir, logDirErr := diaglog.LogDir()
	if logDirErr != nil {
		logDir = ""
	}
	diag := diaglog.New(os.Stderr, level, *path, logDir)
	logger := diag.Logger()
	go diag.Watch(ctx, diaglog.PollInterval, logger.With("component", "diagnostics"))
	mesh.SetDiagnosticLogger(logger.With("component", "mesh"))
	api.SetLogger(logger.With("component", "coordinator"))
	opts := []agent.Option{agent.WithLogger(logger)}
	// HARDENING-PLAN §36.4: contribution must be conditional, so the power,
	// thermal and free-disk probes are installed for every configuration — there
	// is no flag to turn them off, because "unconditional" is the thing §36.4
	// forbids. The data directory (not "/") is measured, since an owner who put
	// their neXal data on an external volume cares about free space there.
	//
	// Off macOS the Mac-only dimensions report unknown and withhold nothing, which
	// is why a Linux end-to-end test is unaffected by this wiring.
	if *idle {
		// --dev-assume-idle already substitutes synthetic idle/memory telemetry, so
		// it substitutes synthetic §36.4 conditions too: a Linux end-to-end test must
		// not fail because the CI box happens to be below the disk floor. Status marks
		// them synthetic, and the flag is refused for a production configuration above.
		opts = append(opts, agent.WithSyntheticConditions())
	} else {
		opts = append(opts, agent.WithConditionSource(agent.NewConditionSource(filepath.Dir(*path))))
	}
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
			// Owner-configured cross-VLAN endpoints, merged into the same candidate
			// view and marked as configured rather than discovered. They add
			// addresses only: AllowedPeers still comes from the coordinator alone.
			StaticPeers: agent.StaticPeersFrom(c.StaticPeers),
		}))
	} else if len(c.StaticPeers) > 0 {
		// Static peers without any discovery gate open: nothing is announced and
		// the coordinator directory is not polled, so no static peer can be
		// authorized — but the owner's configured entries are still merged and
		// visible as "configured, not authorized", which is the honest state.
		opts = append(opts, agent.WithDiscovery(agent.DiscoveryOptions{
			Config:      c.Discovery,
			StaticPeers: agent.StaticPeersFrom(c.StaticPeers),
		}))
	}
	// A paired Mac reports its real tunnel state. The gate re-reads the saved
	// configuration, so a Mac paired while this agent is running starts
	// reporting (and listing its peers) without an agent restart.
	meshProvider := enrollmentGatedMesh{path: *path, inner: mesh.NewRuntimeProvider()}
	opts = append(opts, agent.WithMeshProvider(meshProvider))
	// This Mac's details for the other computers' panels, sent over the
	// presence stream below (one frame, only on material change).
	hostInfo := sysinfo.NewCollector()
	canary := cybersecurity.CanaryForConfig(*path)
	honeypot := cybersecurity.HoneypotForConfig(*path)
	// Live presence (GET /api/v2/hosts/events) and Wake-on-LAN. The stream uses
	// the same client, so the same origin/TLS/no-proxy policy and host token as
	// every REST call. A coordinator without these routes answers 404 and both
	// loops back off quietly; neither affects admission or heartbeats. A relayed
	// wake.request is sent with wol.Send; the online set authorizes nothing.
	// Every magic packet this Mac sends is logged with the interface, source and
	// destination, so a wake that "did nothing" can be traced to the wire.
	wolLog := logger.With("component", "wol")
	wol.SetTrace(func(r wol.Report) {
		for _, a := range r.Attempts {
			level := slog.LevelInfo
			if a.Packets == 0 {
				level = slog.LevelWarn
			}
			wolLog.Log(context.Background(), level, "wake packet", "interface", a.Interface, "source", a.Source,
				"destination", a.Destination, "bound", a.Bound, "limited", a.Limited, "packets", a.Packets,
				"targets", strings.Join(r.MACs, ","), "error", a.Error)
		}
		for _, s := range r.Skipped {
			wolLog.Debug("wake interface skipped", "interface", s.Interface, "address", s.Address, "reason", s.Reason)
		}
		if len(r.Attempts) == 0 {
			wolLog.Warn("wake had no usable interface", "targets", strings.Join(r.MACs, ","), "skipped", len(r.Skipped))
		}
	})
	pres, err := presence.New(presence.Options{HostID: c.HostID, Wake: wol.Send,
		Logger: logger.With("component", "presence"),
		Info: func(ctx context.Context) sysinfo.Info {
			info := hostInfo.Collect(ctx)
			info.ExitNodeStatus = mesh.ReadExitNodeStatus(ctx)
			if state, err := canary.Status(); err == nil {
				info.CanaryStatus = state.Status
				info.CanaryLastCheckedAt = state.LastCheckedAt
			} else {
				info.CanaryStatus = "error"
			}
			// Status and last-trigger time only; source addresses never leave this Mac.
			if state, err := honeypot.Status(); err == nil {
				info.HoneypotStatus = state.Status
				info.HoneypotLastTriggeredAt = state.LastTriggeredAt
			} else {
				info.HoneypotStatus = "error"
			}
			if ip := meshProvider.Snapshot().SelfTunnelAddress; client.ValidTunnelAddress(ip) {
				info.TunnelAddress = ip
			}
			return info
		},
		Dial: func(ctx context.Context) (presence.Conn, error) {
			conn, err := api.DialHostEvents(ctx)
			if err != nil {
				return nil, err // never a typed-nil *wsclient.Conn in the interface
			}
			return conn, nil
		}})
	if err != nil {
		return err
	}
	opts = append(opts, agent.WithPresence(pres), agent.WithWakeInfo(api))
	// Throwaway-host runner (macOS only; opt-in via sandbox-hosting.json). Running
	// VMs are separate launchd jobs, so closing the manager never stops them.
	if sbx := newSandboxManager(logger); sbx != nil {
		opts = append(opts, agent.WithSandbox(sbx))
		defer sbx.Close()
	}
	a, err := agent.New(c, *path, api, probe, *pull, opts...)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(6)
	learningConnected := func() bool {
		status := a.Snapshot()
		observed, err := time.Parse(time.RFC3339Nano, status.Mesh.UpdatedAt)
		return err == nil && time.Since(observed) >= 0 && time.Since(observed) < 30*time.Second && meshProvider.Enrolled() &&
			status.Mesh.ProviderAvailable && status.Mesh.Lifecycle == mesh.LifecycleConnected && !status.Paused &&
			!status.Contribution.Withholding && status.ActiveAttempt == "" && !status.CredentialRejected
	}
	go func() {
		defer wg.Done()
		runLearningWithInterval(ctx, learningConnected, func(parent context.Context) error {
			return api.ValidationTick(parent, c.HostID, canary)
		}, 5*time.Minute, 5*time.Second)
	}()
	go func() {
		defer wg.Done()
		manager := privateruntime.Manager{API: api, HostID: c.HostID, Root: filepath.Join(filepath.Dir(*path), "runtime-sessions"), Connected: learningConnected}
		if err := manager.RunLoop(ctx); err != nil {
			logger.Warn("private runtime bootstrap stopped", "reason", "bootstrap_unavailable")
		}
	}()
	go func() {
		defer wg.Done()
		honeypot.Run(ctx, func(ctx context.Context, event cybersecurity.Event) error {
			return api.ReportSecurityEvent(ctx, c.HostID, event)
		})
	}()
	go func() {
		defer wg.Done()
		runCanary(ctx, canary, func(ctx context.Context, event cybersecurity.Event) error {
			return api.ReportSecurityEvent(ctx, c.HostID, event)
		}, func(ctx context.Context, state cybersecurity.CanaryState) error {
			operation, err := api.ExchangeWatermarkState(ctx, c.HostID, state)
			if err != nil {
				return err
			}
			if state.OperationResult != nil {
				if err := canary.MarkOperationReported(state.OperationResult.ID); err != nil {
					return err
				}
			}
			if operation == nil {
				return nil
			}
			operationCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			if err := canary.ExecuteOperation(operationCtx, *operation, func(ctx context.Context, e cybersecurity.Event) error {
				return api.ReportSecurityEvent(ctx, c.HostID, e)
			}); err != nil {
				return err
			}
			updated, err := canary.Status()
			if err != nil {
				return err
			}
			_, err = api.ExchangeWatermarkState(ctx, c.HostID, updated)
			if err == nil && updated.OperationResult != nil {
				return canary.MarkOperationReported(updated.OperationResult.ID)
			}
			return err
		})
	}()
	go func() { defer wg.Done(); results <- a.Serve(ctx, admin) }()
	go func() { defer wg.Done(); results <- a.Run(ctx) }()
	// Version-2 enrollment uses the embedded peer-to-peer mesh runtime. Older
	// installations may still carry a cloudflared tunnel block in config.json;
	// starting that legacy supervisor as well makes a missing old token abort the
	// local API before the mesh can report status. Keep the legacy path only for
	// pre-v2 configurations that have not migrated to mesh enrollment.
	if shouldRunLegacyTunnel(c) {
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
	var guestDeadline <-chan time.Time
	var guestTimer *time.Timer
	if c.GuestAccess != nil {
		deadline, _ := time.Parse(time.RFC3339Nano, c.GuestAccess.AccessExpiresAt)
		guestTimer = time.NewTimer(time.Until(deadline))
		defer guestTimer.Stop()
		guestDeadline = guestTimer.C
	}
	guestExpired := false
	select {
	case err = <-results:
	case <-ctx.Done():
	case <-guestDeadline:
		guestExpired = true
	}
	cancel()
	wg.Wait()
	if guestExpired {
		request, stop := context.WithTimeout(context.Background(), 12*time.Second)
		defer stop()
		_ = expireGuest(request, *path, &c)
		return guestExpiredError(c.GuestAccess)
	}
	return err
}

func shouldRunLegacyTunnel(c config.Config) bool { return c.Tunnel != nil && c.Enrollment == nil }
func localCommand(ctx context.Context, command string, args []string) error {
	f, path, err := flags(command)
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	method := "POST"
	if command == "status" || command == "policy" || command == "peers-view" {
		method = "GET"
	}
	// peers-view is named to avoid colliding with the existing `peers` command, which
	// manages invitations and enrollment rather than reporting the live view.
	route := command
	if command == "peers-view" {
		route = "peers"
	}
	return localRequest(ctx, *path, method, route, nil)
}

func policyCommand(ctx context.Context, args []string) error {
	f, path, err := flags("set-policy")
	if err != nil {
		return err
	}
	memory := f.Uint64("memory-limit-mib", 0, "workload cap, 64–8192 MiB")
	reserve := f.Uint64("reserve-memory-mib", 0, "owner reserve, 128–1048576 MiB")
	idle := f.Uint64("idle-seconds", 0, "idle threshold, 30–86400 seconds")
	// The upload flags default to auto rather than being required like the three
	// memory/idle settings: existing scripts and the already-built Swift UI call
	// set-policy with three flags, and failing them on upgrade would be worse
	// than inheriting the default the founder asked for anyway. Auto is also the
	// safe reading of silence — the alternative default is an unshaped uplink.
	uploadMode := f.String("upload-mode", config.UploadModeAuto, "upload throttle mode: auto|manual|unlimited")
	uploadLimit := f.Uint64("upload-limit-kib-per-second", 0, "manual upload ceiling in KiB/s, 32–1048576; manual mode only")
	// Optional for the same reason as the upload flags: an existing three-flag
	// caller (including the shipped Swift UI) must keep working, and 0 means the
	// reviewed §36.4 default rather than "no floor".
	minFreeDisk := f.Uint64("min-free-disk-mib", 0, "§36.4 disk reserve in MiB, 1024–1048576; 0 keeps the 10 GiB default")
	if err := parse(f, args, path); err != nil {
		return err
	}
	// Require all fields; never accidentally reset an omitted consent limit.
	if *memory > 8192 || *reserve > 1<<20 {
		return errors.New("memory limits are outside safe bounds")
	}
	if *uploadLimit > 1<<20 {
		return errors.New("upload limit is outside safe bounds")
	}
	if *minFreeDisk > 1<<20 {
		return errors.New("disk reserve is outside safe bounds")
	}
	policy := config.ResourcePolicy{MemoryLimitBytes: *memory << 20,
		ReserveMemoryBytes: *reserve << 20, IdleSeconds: *idle,
		UploadMode: *uploadMode, UploadLimitBytesPerSecond: *uploadLimit << 10,
		MinFreeDiskBytes: *minFreeDisk << 20}
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
	b, err := localFetch(ctx, path, method, command, body, 64<<10)
	if err != nil {
		return err
	}
	var out any
	if json.NewDecoder(bytes.NewReader(b)).Decode(&out) != nil {
		return errors.New("invalid local API JSON")
	}
	return emit(out)
}

// localFetch performs one authenticated local API request and returns the raw
// 200 response body, at most limit bytes.
func localFetch(ctx context.Context, path, method, command string, body []byte, limit int64) ([]byte, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	secrets, err := config.NewSecrets(path, c)
	if err != nil {
		return nil, err
	}
	token, err := secrets.Get(ctx, "admin")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.Listen+"/v1/"+command, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot construct local command")
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
		return nil, errors.New("connector is not running or local API is unavailable")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("invalid local API response")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("local command rejected (HTTP %d)", resp.StatusCode)
	}
	return b, nil
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

// enrollmentGatedMesh exposes the tunnel runtime only while the saved
// configuration holds an enrollment.
type enrollmentGatedMesh struct {
	path  string
	inner mesh.Provider
}

func (p enrollmentGatedMesh) Enrolled() bool {
	c, err := config.Load(p.path)
	return err == nil && c.Enrollment != nil && (c.GuestAccess == nil || !c.GuestAccess.IsExpired(time.Now()))
}

func (p enrollmentGatedMesh) Snapshot() mesh.Status {
	if !p.Enrolled() {
		return mesh.UnavailableProvider{}.Snapshot()
	}
	return p.inner.Snapshot()
}
