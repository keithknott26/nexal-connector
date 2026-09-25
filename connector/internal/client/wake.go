package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"nexal/connector/internal/wsclient"
)

// WakeInfo is what the coordinator needs to have this Mac woken by another
// neXal Mac on the same local network (PUT /api/v2/hosts/wake-info).
type WakeInfo struct {
	// MACs are 1..8 lowercase, colon-separated unicast hardware addresses.
	MACs []string `json:"macs"`
	// LANKey is the hex SHA-256 of this host's sorted private IPv4 network
	// prefixes; the coordinator binds it to the public source address itself.
	LANKey string `json:"lanKey"`
	// WakeForNetwork reports the macOS "Wake for network access" setting.
	WakeForNetwork bool `json:"wakeForNetwork"`
	// TunnelAddress is this host's secure-network IPv4, when known.
	TunnelAddress string `json:"tunnelAddress,omitempty"`
}

// WakeResult is the coordinator's 202 answer to a wake request.
type WakeResult struct {
	RequestID            string `json:"requestId"`
	Relays               int    `json:"relays"`
	TargetWakeForNetwork bool   `json:"targetWakeForNetwork"`
}

// MaxWakeMACs bounds WakeInfo.MACs, matching the coordinator.
const MaxWakeMACs = 8

var tunnelPrefix = netip.MustParsePrefix("100.64.0.0/10")

// ValidTunnelAddress reports whether s is a canonical secure-network IPv4.
func ValidTunnelAddress(s string) bool {
	ip, err := netip.ParseAddr(s)
	return err == nil && ip.Is4() && ip.String() == s && tunnelPrefix.Contains(ip)
}

// ValidWakeMAC reports whether s is a lowercase colon-separated unicast MAC.
func ValidWakeMAC(s string) bool {
	if len(s) != 17 {
		return false
	}
	nonZero := false
	for i := 0; i < 17; i++ {
		c := s[i]
		if i%3 == 2 {
			if c != ':' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
		if c != '0' {
			nonZero = true
		}
	}
	// The low bit of the first octet marks a group (multicast) address.
	first := hexNibble(s[0])<<4 | hexNibble(s[1])
	return nonZero && first&1 == 0
}

func hexNibble(c byte) byte {
	if c >= 'a' {
		return c - 'a' + 10
	}
	return c - '0'
}

func validLANKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Validate applies the coordinator's schema locally, so a bad value never
// leaves this process.
func (w WakeInfo) Validate() error {
	if len(w.MACs) < 1 || len(w.MACs) > MaxWakeMACs {
		return errors.New("wake info needs 1 to 8 hardware addresses")
	}
	seen := map[string]bool{}
	for _, m := range w.MACs {
		if !ValidWakeMAC(m) || seen[m] {
			return errors.New("invalid wake hardware address")
		}
		seen[m] = true
	}
	if !validLANKey(w.LANKey) {
		return errors.New("invalid wake network key")
	}
	if w.TunnelAddress != "" && !ValidTunnelAddress(w.TunnelAddress) {
		return errors.New("invalid wake tunnel address")
	}
	return nil
}

// PutWakeInfo publishes this host's Wake-on-LAN details.
func (c *Client) PutWakeInfo(ctx context.Context, w WakeInfo) error {
	if err := w.Validate(); err != nil {
		return err
	}
	var out struct {
		OK             bool `json:"ok"`
		MACCount       int  `json:"macCount"`
		WakeForNetwork bool `json:"wakeForNetwork"`
	}
	if err := c.call(ctx, "PUT", "/api/v2/hosts/wake-info", w, &out); err != nil {
		return err
	}
	if !out.OK || out.MACCount != len(w.MACs) {
		return errors.New("wake info not accepted")
	}
	return nil
}

// WakeByTunnel asks the coordinator to wake the Mac with this secure-network
// address.
func (c *Client) WakeByTunnel(ctx context.Context, tunnelAddress string) (WakeResult, error) {
	if !ValidTunnelAddress(tunnelAddress) {
		return WakeResult{}, errors.New("invalid wake tunnel address")
	}
	return c.wake(ctx, "/api/v2/hosts/wake", map[string]string{"tunnelAddress": tunnelAddress})
}

// WakeHost asks the coordinator to wake the host with this id.
func (c *Client) WakeHost(ctx context.Context, hostID string) (WakeResult, error) {
	if !ValidID(hostID) {
		return WakeResult{}, errors.New("invalid host id")
	}
	return c.wake(ctx, "/api/v2/hosts/"+url.PathEscape(hostID)+"/wake", nil)
}

func (c *Client) wake(ctx context.Context, path string, body any) (WakeResult, error) {
	var out WakeResult
	if err := c.call(ctx, "POST", path, body, &out); err != nil {
		return WakeResult{}, err
	}
	if !ValidID(out.RequestID) || out.Relays < 0 {
		return WakeResult{}, errors.New("invalid coordinator response schema")
	}
	return out, nil
}

// EventsPath is the coordinator's host event stream.
const EventsPath = "/api/v2/hosts/events"

// DialEvents opens the host event WebSocket with this client's host token.
// It uses wss:// for an https coordinator and ws:// only for the plain-HTTP
// development loopback origin that New already restricts http to. The TLS
// configuration is the one the REST transport uses (TLS 1.3 floor). A non-101
// answer is returned as *StatusError; other failures carry fixed messages only.
func (c *Client) DialEvents(ctx context.Context) (*wsclient.Conn, error) {
	if c.token == "" {
		return nil, errors.New("host credential required for coordinator events")
	}
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, errors.New("invalid coordinator URL")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		if !c.dev {
			return nil, errors.New("HTTPS required for coordinator events")
		}
		u.Scheme = "ws"
	default:
		return nil, errors.New("invalid coordinator URL")
	}
	u.Path = EventsPath
	var tlsConfig *tls.Config
	if tr, ok := c.http.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
		tlsConfig = tr.TLSClientConfig
	} else {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := wsclient.Dial(dctx, u.String(), wsclient.Options{
		Header:     http.Header{"Authorization": {"Bearer " + c.token}},
		TLSConfig:  tlsConfig,
		MaxMessage: 64 << 10,
	})
	if err != nil {
		var he *wsclient.HandshakeError
		switch {
		case errors.As(err, &he):
			return nil, &StatusError{Status: he.Status}
		case errors.Is(err, wsclient.ErrBadHandshake):
			return nil, errors.New("coordinator event stream handshake was invalid")
		}
		return nil, transportError(err)
	}
	return conn, nil
}
