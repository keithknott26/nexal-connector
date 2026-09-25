package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/wol"
	"nexal/connector/internal/wsclient"
)

// eventStream is the part of *wsclient.Conn the events loop uses.
type eventStream interface {
	ReadMessage() (wsclient.MessageType, []byte, error)
	WriteText(string) error
	SetReadDeadline(time.Time) error
	Close(int) error
}

// eventsAPI is implemented by the real coordinator client; test fakes that do
// not implement it simply never connect.
type eventsAPI interface {
	DialEvents(context.Context) (*wsclient.Conn, error)
}

// Coordinator close codes that end the events loop for good.
const (
	closeCredentialGone = 4001
	closeSuperseded     = 4002
)

// maxPresence bounds the in-memory presence set.
const maxPresence = 10000

// Replaced in tests.
var (
	eventsPingInterval = 30 * time.Second
	// Pings are answered with "pong", so a healthy socket is never silent for
	// long; a read idle past this is a dead path.
	eventsReadTimeout = 75 * time.Second
	eventsBackoffMin  = time.Second
	eventsBackoffMax  = 60 * time.Second
	eventsStableAfter = 60 * time.Second
	eventsDialTimeout = 15 * time.Second
	wakeBroadcast     = wol.SendAll
)

// OnlineHosts is the coordinator's current view of which hosts on this
// account are online, from the events stream. It is empty while the stream is
// disconnected. The result is a sorted copy.
func (a *Agent) OnlineHosts() []string {
	a.presenceMu.Lock()
	defer a.presenceMu.Unlock()
	out := make([]string, 0, len(a.online))
	for id := range a.online {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func (a *Agent) setPresence(ids []string) {
	a.presenceMu.Lock()
	defer a.presenceMu.Unlock()
	a.online = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if len(a.online) >= maxPresence {
			break
		}
		if client.ValidID(id) {
			a.online[id] = struct{}{}
		}
	}
}

func (a *Agent) markPresence(id string, online bool) {
	if !client.ValidID(id) {
		return
	}
	a.presenceMu.Lock()
	defer a.presenceMu.Unlock()
	if !online {
		delete(a.online, id)
		return
	}
	if a.online == nil {
		a.online = map[string]struct{}{}
	}
	if len(a.online) < maxPresence {
		a.online[id] = struct{}{}
	}
}

func (a *Agent) eventDialer() func(context.Context) (eventStream, error) {
	if a.dialEvents != nil {
		return a.dialEvents
	}
	api, ok := a.api.(eventsAPI)
	if !ok {
		return nil
	}
	return func(ctx context.Context) (eventStream, error) {
		c, err := api.DialEvents(ctx)
		if err != nil {
			return nil, err // never a typed-nil *wsclient.Conn in the interface
		}
		return c, nil
	}
}

// runEvents keeps the coordinator event stream open: presence updates and
// wake requests for Macs on this LAN. It reconnects with jittered exponential
// backoff, and stops for good when the coordinator says the credential is gone
// (4001) or that a newer socket for this host replaced this one (4002).
func (a *Agent) runEvents(ctx context.Context, hostID string) {
	dial := a.eventDialer()
	if dial == nil {
		return
	}
	backoff := eventsBackoffMin
	for {
		connectedAt, err := a.eventSession(ctx, dial, hostID)
		a.setPresence(nil)
		if ctx.Err() != nil {
			return
		}
		var ce *wsclient.CloseError
		if errors.As(err, &ce) {
			switch ce.Code {
			case closeCredentialGone:
				a.logger.Warn("coordinator events stopped: this host's credential is no longer valid")
				return
			case closeSuperseded:
				a.logger.Info("coordinator events handed over to a newer connection for this host")
				return
			}
		}
		if !connectedAt.IsZero() && time.Since(connectedAt) > eventsStableAfter {
			backoff = eventsBackoffMin
		}
		delay := backoff/2 + rand.N(backoff/2+1)
		if connectedAt.IsZero() {
			a.logger.Debug("coordinator events connect failed", "error", errorText(err), "retry_in", delay.String())
		} else {
			a.logger.Info("coordinator events disconnected", "error", errorText(err), "retry_in", delay.String())
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(backoff*2, eventsBackoffMax)
	}
}

// eventSession runs one connection until it fails. connectedAt is zero when
// the dial itself failed.
func (a *Agent) eventSession(ctx context.Context, dial func(context.Context) (eventStream, error), hostID string) (connectedAt time.Time, err error) {
	dctx, cancelDial := context.WithTimeout(ctx, eventsDialTimeout)
	conn, err := dial(dctx)
	cancelDial()
	if err != nil {
		return time.Time{}, err
	}
	connectedAt = time.Now()
	a.logger.Info("coordinator events connected")
	sessionCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	// Closing the socket is what unblocks ReadMessage on shutdown or when the
	// pinger fails.
	go func() {
		defer wg.Done()
		<-sessionCtx.Done()
		_ = conn.Close(wsclient.CloseGoingAway)
	}()
	go func() {
		defer wg.Done()
		t := time.NewTicker(eventsPingInterval)
		defer t.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-t.C:
				if err := conn.WriteText("ping"); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(eventsReadTimeout))
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return connectedAt, err
		}
		if typ == wsclient.TextMessage {
			a.handleEvent(data, hostID)
		}
	}
}

type coordinatorEvent struct {
	V            int      `json:"v"`
	Type         string   `json:"type"`
	Online       []string `json:"online"`
	HostID       string   `json:"hostId"`
	RequestID    string   `json:"requestId"`
	TargetHostID string   `json:"targetHostId"`
	MACs         []string `json:"macs"`
}

func (a *Agent) handleEvent(data []byte, hostID string) {
	if string(data) == "pong" {
		return
	}
	var e coordinatorEvent
	if err := json.Unmarshal(data, &e); err != nil {
		a.logger.Debug("coordinator event ignored: not a recognised message")
		return
	}
	if e.V != 1 {
		return
	}
	switch e.Type {
	case "snapshot":
		a.setPresence(e.Online)
	case "host.online":
		a.markPresence(e.HostID, true)
	case "host.offline", "host.removed":
		a.markPresence(e.HostID, false)
	case "wake.request":
		a.handleWakeRequest(e, hostID)
	}
}

// handleWakeRequest broadcasts the magic packets for a sleeping Mac that the
// coordinator believes shares this Mac's LAN. There is no acknowledgement.
func (a *Agent) handleWakeRequest(e coordinatorEvent, hostID string) {
	request := e.RequestID
	if !client.ValidID(request) {
		request = "invalid"
	}
	if e.TargetHostID == hostID {
		return // this Mac is the target, and it is plainly awake
	}
	var macs []string
	for _, m := range e.MACs {
		if len(macs) < client.MaxWakeMACs && client.ValidWakeMAC(m) && !slices.Contains(macs, m) {
			macs = append(macs, m)
		}
	}
	if len(macs) == 0 {
		a.logger.Warn("wake request ignored: no valid hardware address", "request", request)
		return
	}
	if err := wakeBroadcast(macs); err != nil {
		a.logger.Warn("wake broadcast failed", "request", request, "error", errorText(err))
		return
	}
	a.logger.Info("wake broadcast sent", "request", request, "addresses", len(macs))
}
