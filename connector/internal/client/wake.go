package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"nexal/connector/internal/observability"
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

// Wake request failures the coordinator names with an error code. Each has a
// fixed message written for the owner; the coordinator's own message is never
// shown (see StatusError).
var (
	// ErrNoWakeRelay is 409 no_wake_relay: no awake neXal Mac shares a LAN with
	// the target.
	ErrNoWakeRelay = errors.New("No awake neXal Mac on that computer's network can wake it.")
	// ErrWakeRateLimited is 429 rate_limited: the coordinator bounds how often
	// one target may be woken.
	ErrWakeRateLimited = errors.New("Too many wake requests for that computer; try again in a minute.")
	// ErrWakeTargetNotFound is 404 wake_target_not_found: no such host in this
	// tenant (or it has been removed).
	ErrWakeTargetNotFound = errors.New("The coordinator does not know that computer; it may have been removed from the network.")
)

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
	if tr, ok := observability.Underlying(c.http.Transport).(*http.Transport); ok && tr.TLSClientConfig != nil {
		return tr.TLSClientConfig.Clone()
	}
	return nil
}

// WakeInfo is the body of PUT /api/v2/hosts/wake-info.
type WakeInfo struct {
	MACs           []string `json:"macs"`
	LANKey         string   `json:"lanKey"`
	WakeForNetwork bool     `json:"wakeForNetwork"`
	// TunnelAddress is this host's own secure-network IPv4 (100.64/10), so a
	// peer that knows it only by that address can name it as a wake target
	// (POST /api/v2/hosts/wake). Omitted when unknown.
	TunnelAddress string `json:"tunnelAddress,omitempty"`
	// LANPrefixes are per-prefix LAN fingerprints (wol.PrefixKeys). Omitted when
	// empty, and dropped automatically for a coordinator that predates them.
	LANPrefixes []string `json:"lanPrefixes,omitempty"`
}

// ValidTunnelAddress reports whether s is a canonical IPv4 in 100.64.0.0/10.
func ValidTunnelAddress(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4() && a.String() == s && netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// ReportWakeInfo publishes this host's wake facts. It is a full replacement,
// so repeating it is idempotent. The response body is not interpreted.
func (c *Client) ReportWakeInfo(ctx context.Context, w WakeInfo) error {
	if len(w.MACs) > wol.MaxMACs || !validDigest(w.LANKey) {
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
	if w.TunnelAddress != "" && !ValidTunnelAddress(w.TunnelAddress) {
		return errors.New("invalid wake info")
	}
	if len(w.LANPrefixes) > wol.MaxPrefixKeys {
		return errors.New("invalid wake info")
	}
	for _, p := range w.LANPrefixes {
		if !validDigest(p) {
			return errors.New("invalid wake info")
		}
	}
	err := c.callLenient(ctx, "PUT", "/api/v2/hosts/wake-info", w, nil)
	// An older coordinator refuses the unknown field with 400 invalid_schema:
	// report again without it rather than stop reporting at all.
	var status *StatusError
	if len(w.LANPrefixes) > 0 && errors.As(err, &status) && status.Status == 400 {
		w.LANPrefixes = nil
		return c.callLenient(ctx, "PUT", "/api/v2/hosts/wake-info", w, nil)
	}
	return err
}

// WakeAck is the coordinator's answer to a wake request: HTTP 202 with
// {requestId, relays, targetWakeForNetwork}. There is no "requested" field;
// any 2xx carrying a valid requestId and a sane relay count is success.
type WakeAck struct {
	RequestID string `json:"requestId"`
	Relays    int    `json:"relays"`
	// TargetWakeForNetwork is the target's last reported "Wake for network
	// access" setting. False means the packet was relayed but the target has
	// said it will probably not wake from it.
	TargetWakeForNetwork bool `json:"targetWakeForNetwork"`
}

// RequestWake asks the coordinator to have an awake Mac on the target's LAN
// send a magic packet. Coded failures map to ErrNoWakeRelay (409
// no_wake_relay), ErrWakeRateLimited (429) and ErrWakeTargetNotFound (404
// wake_target_not_found); a 503 (feature_unavailable, events_unavailable,
// wake_gate_unavailable) or an uncoded 404 from an older coordinator satisfies
// IsNotSupported.
func (c *Client) RequestWake(ctx context.Context, hostID string) (WakeAck, error) {
	if !ValidID(hostID) {
		return WakeAck{}, errors.New("invalid host id")
	}
	return c.requestWake(ctx, "/api/v2/hosts/"+url.PathEscape(hostID)+"/wake", struct{}{})
}

// RequestWakeByTunnel is RequestWake for a target known only by its tunnel
// address (POST /api/v2/hosts/wake {"tunnelAddress"}), which is how the
// menu-bar peer list knows other Macs. Same errors as RequestWake.
func (c *Client) RequestWakeByTunnel(ctx context.Context, tunnelAddress string) (WakeAck, error) {
	if !ValidTunnelAddress(tunnelAddress) {
		return WakeAck{}, errors.New("invalid tunnel address")
	}
	return c.requestWake(ctx, "/api/v2/hosts/wake", map[string]string{"tunnelAddress": tunnelAddress})
}

func (c *Client) requestWake(ctx context.Context, path string, body any) (WakeAck, error) {
	var out WakeAck
	if err := c.callLenient(ctx, "POST", path, body, &out); err != nil {
		var s *StatusError
		if errors.As(err, &s) {
			switch {
			case s.Status == http.StatusConflict && s.Code == "no_wake_relay":
				return WakeAck{}, ErrNoWakeRelay
			case s.Status == http.StatusTooManyRequests:
				return WakeAck{}, ErrWakeRateLimited
			case s.Status == http.StatusNotFound && s.Code == "wake_target_not_found":
				return WakeAck{}, ErrWakeTargetNotFound
			}
		}
		return WakeAck{}, err
	}
	if !ValidID(out.RequestID) || out.Relays < 0 || out.Relays > 1000 {
		return WakeAck{}, errors.New("invalid coordinator wake response")
	}
	return out, nil
}
