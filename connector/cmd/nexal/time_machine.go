package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/timemachine"
)

// setDestination is replaceable in tests. It runs Apple's tmutil as root via
// sudo; the URL carries the SMB password, so it is never logged or emitted.
var setDestination = func(ctx context.Context, smbURL string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", "/usr/bin/tmutil", "setdestination", "-a", smbURL)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return errors.New("tmutil setdestination failed; check that the secure network is connected and approve the administrator prompt")
	}
	return nil
}

var meshLookup func(string) ([]string, error) // nil = system resolver

// timeMachineCommand reports readiness. With a gateway-client configuration and
// -connect it adds the operator gateway as a Time Machine destination.
// The legacy connector-hosted mode still fails closed (no JuiceFS metadata contract).
func timeMachineCommand(ctx context.Context, args []string) error {
	f, path, err := flags("time-machine")
	if err != nil {
		return err
	}
	connect := f.Bool("connect", false, "add the storage gateway as a Time Machine destination")
	dryRun := f.Bool("dry-run", false, "with -connect, validate everything but do not call tmutil")
	if err = parse(f, args, path); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !client.ValidID(cfg.HostID) {
		return errors.New("pair this computer before checking Time Machine")
	}
	secrets, err := config.NewSecrets(*path, cfg)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return err
	}
	api, err := client.New(cfg.Coordinator, token, cfg.Development)
	if err != nil {
		return err
	}
	clientCfg, err := api.TimeMachineClientConfig(ctx, cfg.HostID)
	if err != nil {
		return err
	}
	if clientCfg.Role == timemachine.RoleClient {
		return timeMachineClient(ctx, api, cfg.HostID, clientCfg, *connect, *dryRun)
	}
	if *connect {
		return errors.New("this network is not configured for gateway-backed Time Machine")
	}
	desired, err := api.TimeMachineConfig(ctx, cfg.HostID)
	if err != nil {
		return err
	}
	observation := timemachine.Observation{Platform: runtime.GOOS, LastErrorCode: "juicefs_metadata_unconfigured"}
	status := timemachine.Evaluate(desired, observation)
	// A disabled response has no error and stays disabled. An enabled response is
	// blocked before credentials are minted, so no unusable secret is created.
	if desired.Enabled {
		status.State = "blocked"
		status.DetailCode = "juicefs_metadata_unconfigured"
	}
	if err := api.ReportTimeMachineStatus(ctx, cfg.HostID, status); err != nil {
		return err
	}
	return emit(map[string]any{"timeMachine": status, "action": "Configure a tenant-scoped JuiceFS metadata service and signed privileged helper before enabling this host."})
}

func timeMachineClient(ctx context.Context, api *client.Client, hostID string, c timemachine.ClientConfig, connect, dryRun bool) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.Enabled {
		return emit(map[string]any{"timeMachine": map[string]any{"role": "client", "state": "disabled", "serviceState": c.ServiceState}})
	}
	d := *c.Destination
	view := map[string]any{"role": "client", "serviceState": c.ServiceState, "host": d.Host, "share": d.Share, "quotaBytes": c.QuotaBytes}
	if err := timemachine.ResolvesInsideMesh(d.Host, meshLookup); err != nil {
		view["state"] = "blocked"
		view["detail"] = err.Error()
		return emit(map[string]any{"timeMachine": view})
	}
	if !connect {
		view["state"] = "ready_to_connect"
		view["action"] = "Run: nexal time-machine -connect"
		return emit(map[string]any{"timeMachine": view})
	}
	if runtime.GOOS != "darwin" && !dryRun {
		return errors.New("Time Machine destinations can only be added on macOS")
	}
	credential, err := api.TimeMachineSMBCredential(ctx, hostID, d)
	if err != nil {
		return err
	}
	defer credential.Zero()
	view["destination"] = credential.RedactedURL()
	if dryRun {
		view["state"] = "validated"
		return emit(map[string]any{"timeMachine": view})
	}
	if err := setDestination(ctx, credential.TmutilURL()); err != nil {
		return err
	}
	view["state"] = "destination_added"
	return emit(map[string]any{"timeMachine": view})
}
