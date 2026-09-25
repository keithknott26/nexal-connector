package agent

import (
	"context"
	"slices"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/wol"
)

// wakeInfoAPI is implemented by the real coordinator client; test fakes that
// do not implement it simply never publish.
type wakeInfoAPI interface {
	PutWakeInfo(context.Context, client.WakeInfo) error
}

// Replaced in tests.
var (
	wakeInterfaces   = wol.WakeInterfaces
	wakeForNetwork   = wol.WakeForNetwork
	wakeInfoInterval = time.Minute
	// Unchanged details are re-sent this often so the coordinator's copy (and
	// the public address it binds the LAN key to) stays fresh.
	wakeInfoRefresh = 10 * time.Minute
)

type wakeInfoState struct {
	last    client.WakeInfo
	lastAt  time.Time
	sent    bool
	lastErr string
}

// runWakeInfo publishes this Mac's Wake-on-LAN details at startup and then
// whenever they change, or every wakeInfoRefresh.
func (a *Agent) runWakeInfo(ctx context.Context) {
	api, ok := a.api.(wakeInfoAPI)
	if !ok {
		return
	}
	var st wakeInfoState
	a.publishWakeInfo(ctx, api, &st)
	t := time.NewTicker(wakeInfoInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.publishWakeInfo(ctx, api, &st)
		}
	}
}

// currentWakeInfo computes what to publish; false when this Mac has no
// wakeable interface on a private network.
func (a *Agent) currentWakeInfo() (client.WakeInfo, bool) {
	macs, lanKey := wakeInterfaces()
	if len(macs) == 0 || lanKey == "" {
		return client.WakeInfo{}, false
	}
	a.mu.Lock()
	provider := a.meshProvider
	a.mu.Unlock()
	tunnel := provider.Snapshot().SelfTunnelAddress
	if !client.ValidTunnelAddress(tunnel) {
		tunnel = ""
	}
	return client.WakeInfo{MACs: macs, LANKey: lanKey, WakeForNetwork: wakeForNetwork(), TunnelAddress: tunnel}, true
}

func sameWakeInfo(x, y client.WakeInfo) bool {
	return slices.Equal(x.MACs, y.MACs) && x.LANKey == y.LANKey && x.WakeForNetwork == y.WakeForNetwork && x.TunnelAddress == y.TunnelAddress
}

func (a *Agent) publishWakeInfo(ctx context.Context, api wakeInfoAPI, st *wakeInfoState) {
	info, ok := a.currentWakeInfo()
	if !ok {
		return
	}
	changed := !st.sent || !sameWakeInfo(info, st.last)
	if !changed && time.Since(st.lastAt) < wakeInfoRefresh {
		return
	}
	reqCtx, stop := boundedRequest(ctx, time.Time{})
	defer stop()
	if err := api.PutWakeInfo(reqCtx, info); err != nil {
		if ctx.Err() != nil {
			return
		}
		// Retried next interval; warn once per distinct failure.
		if msg := errorText(err); msg != st.lastErr {
			st.lastErr = msg
			a.logger.Warn("wake-on-LAN details not published", "error", msg)
		} else {
			a.logger.Debug("wake-on-LAN details not published", "error", msg)
		}
		return
	}
	st.last, st.lastAt, st.sent, st.lastErr = info, time.Now(), true, ""
	if changed {
		a.logger.Info("wake-on-LAN details published", "addresses", len(info.MACs),
			"wake_for_network", info.WakeForNetwork, "tunnel", info.TunnelAddress != "")
	}
}
