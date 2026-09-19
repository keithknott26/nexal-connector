package main

import (
	"context"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

func coordinatorCheckCommand(ctx context.Context, args []string) error {
	f, path, err := flags("coordinator-check")
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
	api, err := client.New(c.Coordinator, "", c.Development)
	if err != nil {
		return err
	}
	if err := api.CheckConnectivity(ctx); err != nil {
		return err
	}
	return emit(map[string]any{
		"coordinatorReachable": true, "credentialsRead": false,
		"enrollmentVerified": false, "proxyUsed": false,
	})
}
