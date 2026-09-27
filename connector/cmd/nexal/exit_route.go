package main

import (
	"context"
	"errors"
	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"strings"
	"time"
)

func exitRouteCommand(ctx context.Context, args []string) error {
	flags, path, err := flags("exit-route")
	if err != nil {
		return err
	}
	tunnel := flags.String("tunnel", "", "target peer's secure-network IPv4 address")
	disable := flags.Bool("disable", false, "remove this host's scoped exit route")
	target := flags.String("target-device", "", "stable target UUID, only for disable")
	if err := parse(flags, args, path); err != nil {
		return err
	}
	targetID := strings.ToLower(*target)
	if targetID != "" && (!*disable || !client.ValidExitDeviceID(targetID)) {
		return errors.New("--target-device requires --disable and a valid device UUID")
	}
	if targetID == "" && !client.ValidTunnelAddress(*tunnel) {
		return errors.New("--tunnel must be a secure-network IPv4 address (100.64.0.0/10)")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !client.ValidID(c.HostID) {
		return errors.New("enroll this host before configuring an exit route")
	}
	ctx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
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
	ack, err := api.ConfigureExitRoute(ctx, c.HostID, *tunnel, targetID, !*disable)
	if err != nil {
		return exitRouteError(err)
	}
	return emit(ack)
}

func exitRouteError(err error) error {
	var status *client.StatusError
	if !errors.As(err, &status) {
		return err
	}
	messages := map[string]string{
		"exit_source_unavailable":   "This Mac must be active on its neXal network before configuring an exit route.",
		"exit_target_unavailable":   "That computer is not available as an exit target on this network.",
		"exit_peer_mapping_pending": "Computer identities are still syncing. Try again shortly.",
		"exit_route_unavailable":    "The exit route could not be configured. Retry shortly; local route selection was not changed.",
		"exit_route_pending":        "Your routing choice was saved and is still being applied. Keep the local route off and retry shortly.",
		"exit_route_superseded":     "A newer routing choice replaced this request. Refresh the route status.",
	}
	if message, ok := messages[status.Code]; ok {
		return &codedError{code: status.Code, err: errors.New(message)}
	}
	return err
}
