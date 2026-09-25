package agent

import (
	"context"
	"slices"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/presence"
	"nexal/connector/internal/wol"
)

// Live presence and Wake-on-LAN, wired into the agent lifecycle.
//
// Two independent background loops, neither of which can affect admission,
// heartbeats or attempts:
//
//	presence   the coordinator's WebSocket feed of which hosts are online, and
//	           the relay for wake.request events (internal/presence).
//	wake info  every wakeInfoInterval, re-read this Mac's physical MACs, lanKey
//	           and "Wake for network access" setting, and PUT them to the
//	           coordinator when they changed, so it can route a wake for THIS
//	           Mac to an awake Mac on the same LAN.
//
// Both are reported in /v1/status as the additive "presence" and "wake"
// objects. Neither authorizes anything: the online set is display input, and a
// magic packet wakes a NIC but grants no access.

const (
	// wakeInfoInterval is how often local wake facts are re-read. Interfaces
	// change on DHCP renewals and network moves, which are minutes-scale events;
	// the PUT is only sent when something actually changed.
	wakeInfoInterval = 5 * time.Minute
	// wakeInfoRetryInterval is the shorter wait after a failed report, for the
	// same reason advertiseRetryInterval exists: until one report succeeds, the
	// coordinator cannot wake this Mac at all.
	wakeInfoRetryInterval = time.Minute
	// wakeInfoRefresh re-sends unchanged wake info so the coordinator's
	// public-address binding of lanKey stays current.
	wakeInfoRefresh = 30 * time.Minute
)

// PresenceSource is the running presence client. *presence.Client satisfies it.
type PresenceSource interface {
	Run(ctx context.Context) error
	Snapshot() presence.Snapshot
}

// WakeInfoReporter publishes this host's wake facts. *client.Client satisfies it.
type WakeInfoReporter interface {
	ReportWakeInfo(ctx context.Context, w client.WakeInfo) error
}

// WithPresence installs the live presence stream. Without it status reports
// presence as not running, which is the honest state for tests and libraries.
func WithPresence(p PresenceSource) Option {
	return func(a *Agent) { a.presence = p }
}

// WithWakeInfo installs the coordinator write for wake facts. Without it the
// facts are still collected for status, and "reported" stays false.
func WithWakeInfo(r WakeInfoReporter) Option {
	return func(a *Agent) { a.wakeReporter = r }
}

// PresenceStatus is the "presence" object in /v1/status.
type PresenceStatus struct {
	// Connected is true while the stream is open and has delivered a snapshot.
	Connected bool `json:"connected"`
	// Online is the sorted list of host ids the coordinator last reported
	// online. It is RETAINED while disconnected (read Connected first); it is
	// never null.
	Online []string `json:"online"`
	// UpdatedAt is when Online last changed (RFC 3339, UTC), or "" if never.
	UpdatedAt string `json:"updatedAt"`
	// Detail is a short human reason when not connected, else "".
	Detail string `json:"detail"`
}

// WakeStatus is the "wake" object in /v1/status.
type WakeStatus struct {
	// MACs are this Mac's physical Ethernet/Wi-Fi addresses; never null.
	MACs []string `json:"macs"`
	// WakeForNetwork is "enabled", "disabled" or "unknown" (pmset womp).
	WakeForNetwork string `json:"wakeForNetwork"`
	// Reported is true when the coordinator accepted the CURRENT facts. It is
	// false before the first report, after a failed one, when the coordinator is
	// too old to accept them, and when the facts changed and could not be sent.
	Reported bool `json:"reported"`
}

func (a *Agent) presenceStatusLocked() PresenceStatus {
	if a.presence == nil {
		return PresenceStatus{Online: []string{}, Detail: "live presence is not running"}
	}
	s := a.presence.Snapshot()
	out := PresenceStatus{Connected: s.Connected, Online: s.Online}
	if out.Online == nil {
		out.Online = []string{}
	}
	if !s.UpdatedAt.IsZero() {
		out.UpdatedAt = s.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if !s.Connected {
		out.Detail = s.LastError
		if out.Detail == "" {
			out.Detail = "not connected"
		}
	}
	return out
}

func (a *Agent) wakeStatusLocked() WakeStatus {
	w := a.wake
	w.MACs = slices.Clone(w.MACs)
	if w.MACs == nil {
		w.MACs = []string{}
	}
	if w.WakeForNetwork == "" {
		w.WakeForNetwork = wol.WakeUnknown
	}
	return w
}

// runPresence runs the presence stream until ctx ends. It returns at once when
// presence is not configured.
func (a *Agent) runPresence(ctx context.Context) {
	if a.presence == nil {
		return
	}
	if err := a.presence.Run(ctx); err != nil && ctx.Err() == nil {
		a.logger.Warn("presence stream stopped", "error", errorText(err))
	}
}

// runWakeInfo collects local wake facts on start and every wakeInfoInterval,
// and reports them to the coordinator whenever they change.
func (a *Agent) runWakeInfo(ctx context.Context) {
	var last client.WakeInfo
	var lastAt time.Time
	lastOK := false
	for {
		a.mu.Lock()
		collect := a.wakeFacts
		reporter := a.wakeReporter
		a.mu.Unlock()
		// lanKey is prefixes-only; the coordinator salts it with the source IP it
		// observes on the PUT. See wol.LANKey.
		f := collect(ctx)
		info := client.WakeInfo{MACs: f.MACs, LANKey: f.LANKey, WakeForNetwork: f.WakeForNetwork == wol.WakeEnabled,
			TunnelAddress: a.selfTunnelAddress()}
		// Re-report periodically even when nothing local changed: the coordinator
		// binds lanKey to the public address it observes, which can change
		// (new ISP lease) without any local fact changing.
		changed := !lastOK || !slices.Equal(info.MACs, last.MACs) || info.LANKey != last.LANKey ||
			info.WakeForNetwork != last.WakeForNetwork || info.TunnelAddress != last.TunnelAddress ||
			time.Since(lastAt) >= wakeInfoRefresh
		a.mu.Lock()
		a.wake.MACs = slices.Clone(f.MACs)
		a.wake.WakeForNetwork = f.WakeForNetwork
		if changed {
			a.wake.Reported = false
		}
		a.mu.Unlock()
		wait := wakeInfoInterval
		if reporter != nil && changed && len(info.MACs) > 0 && info.LANKey != "" {
			reqCtx, stop := boundedRequest(ctx, time.Time{})
			err := reporter.ReportWakeInfo(reqCtx, info)
			stop()
			switch {
			case ctx.Err() != nil:
				return
			case err == nil:
				last, lastOK, lastAt = info, true, time.Now()
				a.mu.Lock()
				a.wake.Reported = true
				a.mu.Unlock()
				a.logger.Info("wake info reported", "macs", len(info.MACs), "wake_for_network", f.WakeForNetwork)
			case client.IsNotSupported(err):
				// An older coordinator: expected, not a fault. Checked again at
				// the normal interval in case the coordinator is upgraded.
				a.logger.Debug("wake info not supported by coordinator")
			default:
				a.logger.Warn("wake info report failed", "error", errorText(err))
				wait = wakeInfoRetryInterval
			}
		}
		if a.wakeInfoEvery > 0 {
			wait = a.wakeInfoEvery
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// selfTunnelAddress is this host's own secure-network address from the mesh
// runtime, or "" when unknown or not a tunnel address.
func (a *Agent) selfTunnelAddress() string {
	a.mu.Lock()
	provider := a.meshProvider
	a.mu.Unlock()
	if provider == nil {
		return ""
	}
	if ip := provider.Snapshot().SelfTunnelAddress; client.ValidTunnelAddress(ip) {
		return ip
	}
	return ""
}
