// Package presence keeps a live view of which hosts in this tenant are online,
// from the coordinator's WebSocket feed GET /api/v2/hosts/events, and relays
// Wake-on-LAN requests the coordinator routes to this Mac.
//
// It runs inside the agent (`nexal run`). The feed is:
//
//	{"v":1,"type":"snapshot","online":["<hostId>",...],"at":"<ISO>"}
//	{"v":1,"type":"host.online","hostId":"...","at":"..."}
//	{"v":1,"type":"host.offline","hostId":"...","at":"..."}
//	{"v":1,"type":"host.removed","hostId":"...","at":"..."}
//	{"v":1,"type":"wake.request","requestId":"...","targetHostId":"...","macs":[...],"at":"..."}
//	{"v":1,"type":"host.info","hostId":"...","info":{...},"at":"..."}
//
// and the snapshot may carry "info":{"<hostId>":{...}} for hosts online now.
//
// HOST DETAILS ride this same socket in the other direction: after each
// snapshot this Mac sends {"v":1,"type":"host.info","info":{...}} (OS, chip,
// cores, memory, disk, thermal state, tunnel address), then re-checks every
// InfoCheckInterval and sends again only when something changed materially
// (sysinfo.Material). No extra HTTP request, no polling, no database write.
//
// plus the text "pong" answering our "ping" every PingInterval, and close code
// 4001 when this host has been removed (4002, "superseded by a newer connection
// from this host", is an ordinary disconnect: back off and reconnect).
//
// WHAT THIS VIEW IS AND IS NOT. The online set is the coordinator's opinion of
// who holds a live connection. It is display and routing input only: it
// authorizes nothing, and nothing in the connector's admission path reads it.
// When the stream is down the last set is RETAINED (with Connected=false and
// its UpdatedAt) rather than emptied, the same choice discovery.Rendezvous makes
// for the peer directory: a blip must not make every Mac look offline. Status
// consumers must read Connected before trusting Online.
//
// REMOVAL. host.removed naming this host, or close code 4001, means the owner
// removed this Mac from the network. The loop then stops reconnecting until the
// process restarts, because reconnecting with a revoked credential is a retry
// storm against a server that has already said no.
package presence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/sysinfo"
	"nexal/connector/internal/wol"
	"nexal/connector/internal/wsclient"
)

const (
	// PingInterval is how often the client sends the text frame "ping". The
	// coordinator answers "pong", which keeps idle-timeout middleboxes (and the
	// Worker's own hibernation) from dropping a quiet socket.
	PingInterval = 25 * time.Second
	// ReadTimeout is how long a silent socket is trusted. It spans two missed
	// pongs plus slack, so one lost frame does not tear the stream down.
	ReadTimeout = 2*PingInterval + 10*time.Second
	// MinBackoff and MaxBackoff bound the reconnect delay (before jitter).
	MinBackoff = time.Second
	MaxBackoff = 60 * time.Second
	// UnsupportedBackoff is the wait after an older coordinator answers the
	// upgrade with 404/501/503. The feature is simply absent there; polling it
	// every minute would be noise in the coordinator's logs for no benefit.
	UnsupportedBackoff = 10 * time.Minute
	// CloseRemoved is the coordinator's application close code for "this host
	// was removed". It is the ONLY close code that stops reconnecting.
	CloseRemoved = 4001
	// CloseSuperseded is "superseded by a newer connection from this host". It
	// is NOT removal: the loop waits the normal backoff and reconnects. From
	// here the connector cannot tell a second live agent from its own stale
	// connection racing a reconnect, and a second agent should not exist at all
	// (`nexal run` holds the host-wide machine lock), so the safe reading is a
	// stale duplicate and the correct response is to come back.
	CloseSuperseded = 4002
	// maxOnline bounds the retained set against a hostile or broken server.
	// It is far above any real tenant; the peer directory pages at 200.
	maxOnline = 4096
	// stableAfter is how long a connection must have lasted for the backoff to
	// reset. Resetting on every successful upgrade would let a server that
	// accepts and immediately drops us drive a 1 s reconnect loop forever.
	stableAfter = 30 * time.Second
	// wakeMinSpacing rate-limits relayed wakes. A relay sends at most a few
	// hundred bytes per wake, but an unlimited relay is still an amplifier.
	wakeMinSpacing = time.Second
	// recentWakeIDs bounds the requestId de-duplication memory.
	recentWakeIDs = 64
	// InfoCheckInterval is how often this Mac re-reads its own details. A frame
	// is sent when metrics change, with a five-minute freshness refresh.
	InfoCheckInterval   = time.Minute
	InfoRefreshInterval = 5 * time.Minute
	// maxInfoText bounds each text field accepted from a peer.
	maxInfoText = 120
)

// Conn is the subset of *wsclient.Conn this package uses, so tests can drive
// the loop without a network.
type Conn interface {
	ReadMessage() (wsclient.MessageType, []byte, error)
	WriteText([]byte) error
	SetReadDeadline(time.Time) error
	Close() error
}

// Dialer opens one presence connection.
type Dialer func(ctx context.Context) (Conn, error)

// WakeSender sends magic packets for the given MACs and reports how many
// interfaces carried them. wol.Send satisfies it.
type WakeSender func(macs []net.HardwareAddr) (int, error)

// Options configures a Client.
type Options struct {
	// HostID is this host's own id: a wake.request targeting it is ignored (a
	// sleeping Mac cannot receive it, so one that does is already awake), and a
	// host.removed naming it stops the loop.
	HostID string
	Dial   Dialer
	// Wake is nil to disable relaying.
	Wake WakeSender
	// SelfTest is called when the coordinator nudges this host that its owner's
	// queued capability self-test (distributed-compute, from "Distribute compute
	// test" on the phone) is ready to be picked up now, rather than waiting for
	// the next poll. Nil disables the nudge; the agent still polls on its own
	// interval either way.
	SelfTest func()
	// Info returns this Mac's details for the host.info frame; nil disables it.
	Info func(ctx context.Context) sysinfo.Info
	// InfoEvery overrides InfoCheckInterval (tests).
	InfoEvery time.Duration
	Logger    *slog.Logger
	// Clock and Sleep are injectable for tests; nil means real time.
	Clock func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Snapshot is the thread-safe view exposed to status.
type Snapshot struct {
	// Connected is true while a stream is open and has delivered its first
	// snapshot.
	Connected bool
	// Online is the sorted set of host ids last reported online. It is
	// retained across disconnects; see the package comment.
	Online []string
	// UpdatedAt is when Online last changed from the server's point of view
	// (the last snapshot or delta applied). Zero if none ever arrived.
	UpdatedAt time.Time
	// LastError is a short, fixed, human reason the stream is not connected.
	// Empty while connected.
	LastError string
	// Removed is true once the coordinator said this host was removed.
	Removed bool
	// Infos holds each other host's last reported details, by host id. Kept
	// across disconnects like Online; dropped when a host is removed.
	Infos map[string]sysinfo.Info
}

// Client maintains the presence stream.
type Client struct {
	opts Options

	mu        sync.Mutex
	connected bool
	online    map[string]struct{}
	updatedAt time.Time
	lastError string
	removed   bool
	infos     map[string]sysinfo.Info

	// Wake relay state, touched only by the read loop goroutine.
	lastWake time.Time
	wakeIDs  []string
}

// New validates options and returns a Client that is not yet running.
func New(o Options) (*Client, error) {
	if !client.ValidID(o.HostID) {
		return nil, errors.New("presence requires an enrolled host id")
	}
	if o.Dial == nil {
		return nil, errors.New("presence requires a dialer")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	return &Client{opts: o, online: map[string]struct{}{}, infos: map[string]sysinfo.Info{},
		lastError: "connecting to the coordinator"}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Snapshot returns a copy of the current view.
func (c *Client) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	online := make([]string, 0, len(c.online))
	for id := range c.online {
		online = append(online, id)
	}
	sort.Strings(online)
	infos := make(map[string]sysinfo.Info, len(c.infos))
	for id, info := range c.infos {
		infos[id] = info
	}
	s := Snapshot{Connected: c.connected, Online: online, UpdatedAt: c.updatedAt, Removed: c.removed, Infos: infos}
	if !c.connected {
		s.LastError = c.lastError
	}
	return s
}

// Run connects and reconnects until ctx is cancelled or this host is removed.
// It always returns nil on cancellation; the reason for any disconnect is in
// Snapshot().LastError and the log.
func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	for ctx.Err() == nil {
		started := c.opts.Clock()
		err := c.session(ctx)
		if ctx.Err() != nil {
			c.setDisconnected("stopped")
			return nil
		}
		if c.isRemoved() {
			c.opts.Logger.Warn("presence stopped: this host was removed from the network; restart neXal after re-adding it")
			return nil
		}
		wait := time.Duration(0)
		reason := describe(err)
		if client.IsNotSupported(err) {
			reason = "this coordinator does not offer live presence yet"
			wait = UnsupportedBackoff
			c.opts.Logger.Debug("presence stream unsupported by coordinator")
		} else {
			if c.opts.Clock().Sub(started) >= stableAfter {
				attempt = 0
			}
			wait = backoff(attempt)
			attempt++
			c.opts.Logger.Info("presence stream disconnected", "reason", reason, "retry_in", wait.Round(time.Millisecond).String())
		}
		c.setDisconnected(reason)
		if c.opts.Sleep(ctx, wait) != nil {
			c.setDisconnected("stopped")
			return nil
		}
	}
	c.setDisconnected("stopped")
	return nil
}

// backoff is exponential from MinBackoff to MaxBackoff with "equal jitter": half
// the step is fixed and half is random, so a fleet reconnecting after a
// coordinator deploy spreads out instead of arriving in lockstep, while no
// single client ever retries sooner than half a step.
func backoff(attempt int) time.Duration {
	d := MaxBackoff
	if attempt < 6 { // 1s<<6 = 64s already exceeds the cap
		d = MinBackoff << uint(attempt)
		if d > MaxBackoff {
			d = MaxBackoff
		}
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// describe maps an error to a short fixed reason for status. Errors reaching
// here are already sanitized by internal/client, but status is shown to the
// owner, so only a fixed vocabulary is used.
func describe(err error) string {
	var ce *wsclient.CloseError
	var se *client.StatusError
	var ne net.Error
	switch {
	case err == nil:
		return "the coordinator closed the presence stream"
	case errors.As(err, &se) && (se.Status == 401 || se.Status == 403):
		return "the coordinator did not accept this host's credential"
	case errors.As(err, &se):
		return "the coordinator refused the presence stream"
	case errors.As(err, &ce) && ce.Code == CloseSuperseded:
		return "another connection from this Mac replaced the presence stream"
	case errors.As(err, &ce):
		return "the coordinator closed the presence stream"
	case errors.Is(err, wsclient.ErrProtocol), errors.Is(err, wsclient.ErrMessageTooBig):
		return "the presence stream sent invalid data"
	case errors.As(err, &ne) && ne.Timeout():
		return "the presence stream went silent"
	}
	return "cannot reach the coordinator"
}

func (c *Client) isRemoved() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removed
}

func (c *Client) setDisconnected(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	if c.removed {
		c.lastError = "this Mac was removed from the network"
		return
	}
	c.lastError = reason
}

// session runs one connection until it fails.
func (c *Client) session(ctx context.Context) error {
	conn, err := c.opts.Dial(ctx)
	if err != nil {
		c.opts.Logger.Debug("presence stream dial failed", "reason", describe(err))
		return err
	}
	c.opts.Logger.Info("presence stream connected")
	sessCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		_ = conn.Close() // also unblocks a ReadMessage in progress
		wg.Wait()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(PingInterval)
		defer t.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-t.C:
				if err := conn.WriteText([]byte("ping")); err != nil {
					// The read side will observe the dead socket; closing here
					// makes that immediate instead of waiting for ReadTimeout.
					_ = conn.Close()
					return
				}
			}
		}
	}()
	// Host details go out once the first snapshot has arrived (the stream is
	// then known to be accepted), and again only on material change.
	snapshotSeen := make(chan struct{})
	var snapshotOnce sync.Once
	if c.opts.Info != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.reportInfo(sessCtx, conn, snapshotSeen)
		}()
	}
	// Cancellation must interrupt a blocked read.
	stop := context.AfterFunc(sessCtx, func() { _ = conn.Close() })
	defer stop()
	for {
		_ = conn.SetReadDeadline(c.opts.Clock().Add(ReadTimeout))
		typ, data, err := conn.ReadMessage()
		if err != nil {
			var ce *wsclient.CloseError
			// Only 4001 means removal. 4002 (superseded) and every other
			// code fall through to the ordinary backoff-and-reconnect path.
			if errors.As(err, &ce) && ce.Code == CloseRemoved {
				c.markRemoved()
			}
			return err
		}
		if typ != wsclient.TextMessage {
			continue // the feed is text-only; ignore rather than fail
		}
		if bytes.Equal(data, []byte("pong")) {
			continue
		}
		if c.handle(data) {
			snapshotOnce.Do(func() {
				close(snapshotSeen)
				c.opts.Logger.Debug("presence snapshot received", "online", len(c.Snapshot().Online))
			})
		}
		if c.isRemoved() {
			return nil
		}
	}
}

func (c *Client) markRemoved() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed = true
	c.connected = false
	c.lastError = "this Mac was removed from the network"
	c.online = map[string]struct{}{}
	c.updatedAt = c.opts.Clock()
}

// event is decoded leniently: unknown fields and unknown types are ignored so
// the coordinator can extend the feed without breaking installed connectors
// (HARDENING-PLAN §43). Every field that IS used is validated.
type event struct {
	V            int      `json:"v"`
	Type         string   `json:"type"`
	Online       []string `json:"online"`
	HostID       string   `json:"hostId"`
	RequestID    string   `json:"requestId"`
	TargetHostID string   `json:"targetHostId"`
	MACs         []string `json:"macs"`
	JobID        string   `json:"jobId"`
	// Info is one host's details (host.info); Infos is the snapshot's map.
	Info  *sysinfo.Info           `json:"info"`
	Infos map[string]sysinfo.Info `json:"infos"`
}

// handle applies one event and reports whether it was a snapshot.
func (c *Client) handle(data []byte) bool {
	var e event
	if err := json.Unmarshal(data, &e); err != nil || e.V != 1 {
		c.opts.Logger.Debug("presence event ignored", "reason", "not a v1 JSON event")
		return false
	}
	switch e.Type {
	case "snapshot":
		if len(e.Online) > maxOnline {
			c.opts.Logger.Warn("presence snapshot ignored", "reason", "too many hosts")
			return false
		}
		next := make(map[string]struct{}, len(e.Online))
		for _, id := range e.Online {
			if !client.ValidID(id) {
				c.opts.Logger.Warn("presence snapshot ignored", "reason", "invalid host id")
				return false
			}
			next[id] = struct{}{}
		}
		c.mu.Lock()
		c.online = next
		c.connected = true
		c.lastError = ""
		c.updatedAt = c.opts.Clock()
		for id, info := range e.Infos {
			if client.ValidID(id) && id != c.opts.HostID && len(c.infos) < maxOnline {
				c.infos[id] = cleanInfo(info)
			}
		}
		c.mu.Unlock()
		return true
	case "host.online", "host.offline", "host.removed":
		if !client.ValidID(e.HostID) {
			c.opts.Logger.Debug("presence event ignored", "reason", "invalid host id")
			return false
		}
		if e.Type == "host.removed" && e.HostID == c.opts.HostID {
			c.markRemoved()
			return false
		}
		c.mu.Lock()
		if e.Type == "host.online" {
			if len(c.online) < maxOnline {
				c.online[e.HostID] = struct{}{}
			}
		} else {
			delete(c.online, e.HostID)
		}
		if e.Type == "host.removed" {
			delete(c.infos, e.HostID)
		}
		c.updatedAt = c.opts.Clock()
		c.mu.Unlock()
	case "host.info":
		if !client.ValidID(e.HostID) || e.HostID == c.opts.HostID || e.Info == nil {
			return false
		}
		c.mu.Lock()
		if _, known := c.infos[e.HostID]; known || len(c.infos) < maxOnline {
			c.infos[e.HostID] = cleanInfo(*e.Info)
		}
		c.mu.Unlock()
	case "wake.request":
		c.relayWake(e)
	case "selftest.request":
		if client.ValidID(e.TargetHostID) && e.TargetHostID == c.opts.HostID && c.opts.SelfTest != nil {
			c.opts.SelfTest()
		}
	default:
		// A newer coordinator's event type; not an error.
	}
	return false
}

// reportInfo sends this Mac's details after the first snapshot, then re-checks
// every InfoCheckInterval and sends changes or a five-minute freshness refresh.
func (c *Client) reportInfo(ctx context.Context, conn Conn, snapshotSeen <-chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case <-snapshotSeen:
	}
	every := c.opts.InfoEvery
	if every <= 0 {
		every = InfoCheckInterval
	}
	var last sysinfo.Info
	var lastSent time.Time
	sent := false
	for {
		info := c.opts.Info(ctx)
		if ctx.Err() != nil {
			return
		}
		if shouldReportInfo(last, info, sent, time.Since(lastSent)) {
			frame, err := json.Marshal(map[string]any{"v": 1, "type": "host.info", "info": info})
			if err == nil && conn.WriteText(frame) == nil {
				last, sent, lastSent = info, true, time.Now()
			}
		}
		if c.opts.Sleep(ctx, every) != nil {
			return
		}
	}
}

// cleanInfo bounds every text field received from a peer.
func cleanInfo(info sysinfo.Info) sysinfo.Info {
	for _, field := range []*string{&info.Name, &info.OS, &info.Model, &info.Chip, &info.Thermal, &info.TunnelAddress, &info.LANAddress,
		&info.BatteryState, &info.PublicIP, &info.Location, &info.ReportedAt, &info.LastTimeMachineBackupAt, &info.CanaryStatus, &info.CanaryLastCheckedAt, &info.HoneypotStatus, &info.HoneypotLastTriggeredAt, &info.ExitNodeStatus} {
		text := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, *field)
		if len(text) > maxInfoText {
			text = text[:maxInfoText]
		}
		*field = text
	}
	return info
}

// relayWake sends the magic packet for a wake the coordinator routed here
// because this Mac shares a LAN with the target. The request carries MACs, not
// a credential: a magic packet wakes a NIC and grants nothing, so the worst a
// forged request can do is wake a machine — which is why it is still bounded
// (MAC count, strict parsing, de-duplication and a rate limit).
func (c *Client) relayWake(e event) {
	log := c.opts.Logger
	log.Info("wake request received", "request", e.RequestID, "target", e.TargetHostID, "macs", len(e.MACs), "relayEnabled", c.opts.Wake != nil)
	if c.opts.Wake == nil {
		return
	}
	if !client.ValidID(e.RequestID) || !client.ValidID(e.TargetHostID) || len(e.MACs) < 1 || len(e.MACs) > wol.MaxMACs {
		log.Warn("wake request ignored", "reason", "invalid request")
		return
	}
	if e.TargetHostID == c.opts.HostID {
		// We are the target and evidently awake; nothing to do.
		log.Debug("wake request ignored", "request", e.RequestID, "reason", "this host is the target")
		return
	}
	for _, id := range c.wakeIDs {
		if id == e.RequestID {
			log.Debug("wake request ignored", "request", e.RequestID, "reason", "duplicate")
			return
		}
	}
	now := c.opts.Clock()
	if !c.lastWake.IsZero() && now.Sub(c.lastWake) < wakeMinSpacing {
		log.Warn("wake request ignored", "request", e.RequestID, "reason", "rate limited")
		return
	}
	macs := make([]net.HardwareAddr, 0, len(e.MACs))
	for _, s := range e.MACs {
		m, err := wol.ParseMAC(s)
		if err != nil {
			log.Warn("wake request ignored", "request", e.RequestID, "reason", "invalid MAC address")
			return
		}
		macs = append(macs, m)
	}
	c.lastWake = now
	c.wakeIDs = append(c.wakeIDs, e.RequestID)
	if len(c.wakeIDs) > recentWakeIDs {
		c.wakeIDs = c.wakeIDs[len(c.wakeIDs)-recentWakeIDs:]
	}
	n, err := c.opts.Wake(macs)
	if err != nil {
		log.Warn("wake relay failed", "request", e.RequestID, "target", e.TargetHostID, "error", err.Error())
		return
	}
	log.Info("wake relayed", "request", e.RequestID, "target", e.TargetHostID, "macs", len(macs), "interfaces", n)
}

// shouldReportInfo keeps local sampling independent from network reporting.
func shouldReportInfo(last, next sysinfo.Info, sent bool, elapsed time.Duration) bool {
	return !sent || elapsed >= InfoRefreshInterval || sysinfo.Material(last, next)
}
