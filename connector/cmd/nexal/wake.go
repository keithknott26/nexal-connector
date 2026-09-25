package main

import (
	"context"
	"errors"
	"net/netip"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/wol"
)

// wakeCommand asks the coordinator to wake a sleeping peer. The coordinator
// hands the request to awake connectors on the peer's network, which broadcast
// the magic packet; when this Mac shares that network it broadcasts too.
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
		if err != nil || !ip.Is4() || !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
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
	res, err := api.RequestWake(ctx, cfg.HostID, *target, *tunnel)
	if err != nil {
		var status *client.StatusError
		if errors.As(err, &status) {
			switch status.Status {
			case 404:
				return errors.New("that Mac is not on your neXal network")
			case 409:
				return errors.New("that Mac has not reported a network interface that can be woken; open neXal Connector on it once while it is awake")
			case 429:
				return errors.New("too many wake requests; try again shortly")
			}
		}
		return err
	}
	sentLocally := false
	if res.SameNetwork {
		broadcast := ""
		if res.Broadcast != nil {
			broadcast = *res.Broadcast
		}
		sentLocally = wol.Send(res.MAC, broadcast) == nil
	}
	return emit(map[string]any{
		"ok": true, "requestId": res.RequestID, "targetOnline": res.TargetOnline,
		"relays": res.Relays, "sentLocally": sentLocally,
	})
}
