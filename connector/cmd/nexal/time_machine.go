package main

import (
	"context"
	"errors"
	"runtime"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/timemachine"
)

// timeMachineCommand reads and reports the paid feature's honest readiness.
// It cannot provision yet: the deployed contract supplies R2 credentials but no
// JuiceFS metadata service, without which JuiceFS has no filesystem to mount.
func timeMachineCommand(ctx context.Context, args []string) error {
	f, path, err := flags("time-machine")
	if err != nil {
		return err
	}
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
