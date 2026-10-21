package client

import (
	"context"
	"errors"
)

// RequestBandwidthOpen asks the coordinator to tell the peer at tunnelAddress to open its bandwidth
// port for one test:
//
//	POST /api/v2/hosts/bandwidth-open   {"tunnelAddress": "100.x.y.z"}
//
// The runner knows peers by mesh peer and tunnel address, not by coordinator host id, so the
// coordinator resolves the address to the host that published it (as wake does). A 202 means the
// request reached a live socket of that peer. It does not prove the port is open; the test
// confirms that by connecting.
func (c *Client) RequestBandwidthOpen(ctx context.Context, tunnelAddress string) error {
	_, err := c.RequestBandwidthOpenID(ctx, tunnelAddress)
	return err
}

// RequestBandwidthOpenID is RequestBandwidthOpen that also returns the coordinator's request id.
// The peer receives the same id, and both sides use it to pick the test's port.
func (c *Client) RequestBandwidthOpenID(ctx context.Context, tunnelAddress string) (string, error) {
	if !ValidTunnelAddress(tunnelAddress) {
		return "", errors.New("invalid tunnel address")
	}
	var out struct {
		RequestID string `json:"requestId"`
		Delivered bool   `json:"delivered"`
	}
	if err := c.callLenient(ctx, "POST", "/api/v2/hosts/bandwidth-open", map[string]string{"tunnelAddress": tunnelAddress}, &out); err != nil {
		return "", err
	}
	if !ValidID(out.RequestID) {
		return "", nil
	}
	return out.RequestID, nil
}
