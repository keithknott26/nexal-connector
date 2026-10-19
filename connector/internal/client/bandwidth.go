package client

import (
	"context"
	"errors"
	"net/url"
)

// RequestBandwidthOpen asks the coordinator to tell peer hostID to open its bandwidth port
// for one test:
//
//	POST /api/v2/hosts/{hostId}/bandwidth-open
//
// A 202 means the request reached a live socket of that peer. It does not prove the port is
// open; the test confirms that by connecting.
func (c *Client) RequestBandwidthOpen(ctx context.Context, hostID string) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	var out struct {
		RequestID string `json:"requestId"`
		Delivered bool   `json:"delivered"`
	}
	return c.callLenient(ctx, "POST", "/api/v2/hosts/"+url.PathEscape(hostID)+"/bandwidth-open", struct{}{}, &out)
}
