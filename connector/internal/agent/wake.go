package agent

import (
	"context"
	"net/netip"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/wol"
)

// wakeRelayAPI is implemented by the real coordinator client; test fakes that
// do not implement it simply never relay.
type wakeRelayAPI interface {
	PendingWakes(context.Context, string) ([]client.WakeRequest, error)
	WakeSent(context.Context, string, string) error
}

const wakePollInterval = 10 * time.Second

// Replaced in tests.
var (
	localWakeInterface = wol.Local
	sendMagicPacket    = wol.Send
)

// wakeInfo is reported on every heartbeat: the interface to wake this Mac on,
// and its tunnel address. Nil when neither is known.
func wakeInfo(tunnel string) *client.WakeInfo {
	info := client.WakeInfo{}
	if ifc, ok := localWakeInterface(); ok {
		info.MAC, info.Broadcast = ifc.MAC, ifc.Broadcast
	}
	if ip, err := netip.ParseAddr(tunnel); err == nil && ip.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		info.Tunnel = ip.String()
	}
	if info.MAC == "" && info.Tunnel == "" {
		return nil
	}
	return &info
}

// runWakeRelay polls for wake requests addressed to this host's network and
// broadcasts each once. Errors are logged and retried on the next tick.
func (a *Agent) runWakeRelay(ctx context.Context, hostID string) {
	relay, ok := a.api.(wakeRelayAPI)
	if !ok {
		return
	}
	t := time.NewTicker(wakePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.relayWakes(ctx, relay, hostID)
	}
}

func (a *Agent) relayWakes(ctx context.Context, relay wakeRelayAPI, hostID string) {
	reqCtx, stop := boundedRequest(ctx, time.Time{})
	defer stop()
	pending, err := relay.PendingWakes(reqCtx, hostID)
	if err != nil {
		a.logger.Debug("wake poll failed", "error", errorText(err))
		return
	}
	for _, w := range pending {
		broadcast := ""
		if w.Broadcast != nil {
			broadcast = *w.Broadcast
		}
		if err := sendMagicPacket(w.MAC, broadcast); err != nil {
			a.logger.Warn("wake broadcast failed", "request", w.ID, "error", errorText(err))
			continue
		}
		a.logger.Info("wake broadcast sent", "request", w.ID)
		if err := relay.WakeSent(reqCtx, hostID, w.ID); err != nil {
			a.logger.Debug("wake acknowledgement failed", "request", w.ID, "error", errorText(err))
		}
	}
}
