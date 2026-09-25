package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"time"

	"nexal/connector/internal/wol"
	"nexal/connector/internal/wsclient"
)

// Live presence and Wake-on-LAN: the three v2 host endpoints the connector uses.
//
//	GET  /api/v2/hosts/events         WebSocket; see internal/presence
//	PUT  /api/v2/hosts/wake-info      this Mac's MACs, lanKey, wake setting
//	POST /api/v2/hosts/{hostId}/wake  ask the coordinator to relay a wake
//
// All three authenticate with the host bearer token, exactly like heartbeat,
// and all three are newer than every other route here, so an older coordinator
// answers 404 (or 503 while a feature flag is off). IsNotSupported names that
// case so callers can stay quiet about it instead of logging a fault.

// ErrNoWakeRelay is returned by RequestWake for the coordinator's 409
// no_wake_relay: no awake neXal Mac shares a LAN with the target.
var ErrNoWakeRelay = errors.New("No awake neXal Mac on that computer's network can wake it.")

// IsNotSupported reports whether err is an older coordinator's "no such
// route" (404), "not implemented" (501) or "feature unavailable" (503).
func IsNotSupported(err error) bool {
	var s *StatusError
	return errors.As(err, &s) && (s.Status == http.StatusNotFound || s.Status == http.StatusNotImplemented ||
		s.Status == http.StatusServiceUnavailable)
}

// DialHostEvents opens the presence WebSocket. It reuses this client's origin,
// which client.New already validated with config.ValidateURL, and its dev flag,
// which is the only thing that permits plain-http numeric loopback — so the
// socket obeys exactly the same origin policy as every REST call. Errors are
// reduced to the same fixed messages: a refused upgrade becomes a StatusError
// (status only), and transport failures go through transportError.
func (c *Client) DialHostEvents(ctx context.Context) (*wsclient.Conn, error) {
	if c.token == "" {
		return nil, errors.New("host credential required for the presence stream")
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	conn, err := wsclient.Dial(ctx, wsclient.Options{URL: c.base + "/api/v2/hosts/events", Header: h,
		AllowLoopbackHTTP: c.dev, TLSConfig: c.tlsConfig(), HandshakeTimeout: 10 * time.Second})
	if err != nil {
		var hs *wsclient.HandshakeError
		switch {
		case errors.As(err, &hs):
			return nil, &StatusError{Status: hs.Status}
		case errors.Is(err, wsclient.ErrProtocol):
			return nil, errors.New("coordinator presence stream failed [protocol]: the server did not complete a WebSocket upgrade")
		}
		return nil, transportError(err)
	}
	return conn, nil
}

// tlsConfig returns the REST transport's TLS settings so the WebSocket trusts
// exactly the roots the REST calls trust (tests install a private root there).
func (c *Client) tlsConfig() *tls.Config {
	if tr, ok := c.http.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
		return tr.TLSClientConfig.Clone()
	}
	return nil
}

// WakeInfo is the body of PUT /api/v2/hosts/wake-info.
type WakeInfo struct {
	MACs           []string `json:"macs"`
	LANKey         string   `json:"lanKey"`
	WakeForNetwork bool     `json:"wakeForNetwork"`
}

// ReportWakeInfo publishes this host's wake facts. It is a full replacement,
// so repeating it is idempotent. The response body is not interpreted.
func (c *Client) ReportWakeInfo(ctx context.Context, w WakeInfo) error {
	if len(w.MACs) < 1 || len(w.MACs) > wol.MaxMACs || !validDigest(w.LANKey) {
		return errors.New("invalid wake info")
	}
	macs := make([]string, 0, len(w.MACs))
	for _, m := range w.MACs {
		hw, err := wol.ParseMAC(m)
		if err != nil {
			return errors.New("invalid wake info")
		}
		macs = append(macs, hw.String())
	}
	w.MACs = macs
	return c.callLenient(ctx, "PUT", "/api/v2/hosts/wake-info", w, nil)
}

// WakeAck is the coordinator's answer to a wake request.
type WakeAck struct {
	Requested bool   `json:"requested"`
	RequestID string `json:"requestId"`
	Relays    int    `json:"relays"`
}

// RequestWake asks the coordinator to have an awake Mac on the target's LAN
// send a magic packet. It returns ErrNoWakeRelay for 409 no_wake_relay.
func (c *Client) RequestWake(ctx context.Context, hostID string) (WakeAck, error) {
	var out WakeAck
	if !ValidID(hostID) {
		return out, errors.New("invalid host id")
	}
	if err := c.callLenient(ctx, "POST", "/api/v2/hosts/"+url.PathEscape(hostID)+"/wake", struct{}{}, &out); err != nil {
		var s *StatusError
		if errors.As(err, &s) && s.Status == http.StatusConflict && s.Code == "no_wake_relay" {
			return WakeAck{}, ErrNoWakeRelay
		}
		return WakeAck{}, err
	}
	if !out.Requested || !ValidID(out.RequestID) || out.Relays < 0 || out.Relays > 1000 {
		return WakeAck{}, errors.New("invalid coordinator wake response")
	}
	return out, nil
}
