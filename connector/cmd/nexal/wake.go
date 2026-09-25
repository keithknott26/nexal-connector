package main

import (
	"context"
	"errors"
	"net/netip"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

// wakeCommand asks the coordinator to wake a sleeping peer. The coordinator
// pushes the request over the event stream to awake neXal Macs on the
// target's local network, which broadcast the magic packet. This Mac sends
// nothing itself: if it shares that network it is one of those relays.
//
//	nexal wake --tunnel 100.113.99.69 --config /abs/config.json
//	nexal wake --host host_... --config /abs/config.json
func wakeCommand(ctx context.Context, args []string) error {
	f, path, err := flags("wake")
	if err != nil {
		return err
	}
	tunnel := f.String("tunnel", "", "target's secure-network address")
	target := f.String("host", "", "target host id")
	if err = parse(f, args, path); err != nil {
		return err
	}
	if (*tunnel == "") == (*target == "") {
		return errors.New("provide exactly one of --tunnel or --host")
	}
	if *tunnel != "" {
		ip, err := netip.ParseAddr(*tunnel)
		if err != nil || !ip.Is4() || !client.ValidTunnelAddress(ip.String()) {
			return errors.New("--tunnel must be a secure-network IPv4 address")
		}
		*tunnel = ip.String()
	} else if !client.ValidID(*target) {
		return errors.New("invalid --host")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !client.ValidID(cfg.HostID) {
		return errors.New("pair this computer before waking another")
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
	var res client.WakeResult
	if *tunnel != "" {
		res, err = api.WakeByTunnel(ctx, *tunnel)
	} else {
		res, err = api.WakeHost(ctx, *target)
	}
	if err != nil {
		return wakeError(err)
	}
	return emit(wakeOutput{OK: true, RequestID: res.RequestID, Relays: res.Relays, TargetWakeForNetwork: res.TargetWakeForNetwork})
}

type wakeOutput struct {
	OK                   bool   `json:"ok"`
	RequestID            string `json:"requestId"`
	Relays               int    `json:"relays"`
	TargetWakeForNetwork bool   `json:"targetWakeForNetwork"`
}

// wakeError turns the coordinator's wake refusals into owner-facing text.
func wakeError(err error) error {
	var status *client.StatusError
	if errors.As(err, &status) {
		switch status.Status {
		case 404:
			return errors.New("that Mac has not reported Wake-on-LAN details yet, or is not on your network")
		case 409:
			return errors.New("no other neXal Mac on that Mac's local network is awake to send the wake packet")
		case 429:
			return errors.New("too many wake requests; wait a minute")
		case 503:
			return errors.New("wake is temporarily unavailable")
		}
	}
	return err
}
