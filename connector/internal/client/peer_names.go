package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"unicode"
)

// Peer names (coordinator migration 0089): how this tenant's apps display a peer of
// the secure network, keyed by the peer's mesh IPv4. The Mac app reads and sets them
// through the connector because it only holds the host token.

// MaxPeerName matches the coordinator's limit.
const MaxPeerName = 60

var meshPrefix = netip.MustParsePrefix("100.64.0.0/10")

// ValidPeerAddress reports whether s is a mesh (100.64.0.0/10) IPv4 address.
func ValidPeerAddress(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4() && meshPrefix.Contains(a)
}

// CleanPeerName trims a name and checks it: 0..60 characters (0 removes the name), no control characters.
func CleanPeerName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > MaxPeerName {
		return "", errors.New("names are at most 60 characters")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("names cannot contain control characters")
		}
	}
	return s, nil
}

// PeerNames is GET /api/v2/hosts/:id/peer-names: {"names":[{"address","name","updatedAt"}]}.
func (c *Client) PeerNames(ctx context.Context, hostID string) (json.RawMessage, error) {
	p, err := sandboxPath(hostID, "peer-names")
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := c.callLenient(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetPeerName is PUT /api/v2/hosts/:id/peer-names/:address. An empty name removes it.
func (c *Client) SetPeerName(ctx context.Context, hostID, address, name string) (json.RawMessage, error) {
	if !ValidPeerAddress(address) {
		return nil, errors.New("invalid peer address")
	}
	clean, err := CleanPeerName(name)
	if err != nil {
		return nil, err
	}
	p, err := sandboxPath(hostID, "peer-names/"+url.PathEscape(address))
	if err != nil {
		return nil, err
	}
	in := struct {
		Name *string `json:"name"`
	}{}
	if clean != "" {
		in.Name = &clean
	}
	var out json.RawMessage
	if err := c.callLenient(ctx, "PUT", p, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}
