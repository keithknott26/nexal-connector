package client

import (
	"context"
	"errors"
	"net/url"
)

// WakeResult is the coordinator's answer to a wake request.
type WakeResult struct {
	OK           bool    `json:"ok"`
	RequestID    string  `json:"requestId"`
	TargetOnline bool    `json:"targetOnline"`
	Relays       int     `json:"relays"`
	SameNetwork  bool    `json:"sameNetwork"`
	MAC          string  `json:"mac"`
	Broadcast    *string `json:"broadcast"`
}

// WakeRequest is a pending wake this host should broadcast on its LAN.
type WakeRequest struct {
	ID        string  `json:"id"`
	MAC       string  `json:"mac"`
	Broadcast *string `json:"broadcast"`
}

// RequestWake asks the coordinator to wake the host at tunnelAddress (or with
// hostID, when tunnelAddress is empty).
func (c *Client) RequestWake(ctx context.Context, host, targetHostID, tunnelAddress string) (WakeResult, error) {
	var out WakeResult
	if !ValidID(host) {
		return out, errors.New("invalid host id")
	}
	body := map[string]string{}
	switch {
	case tunnelAddress != "":
		body["targetTunnelAddress"] = tunnelAddress
	case ValidID(targetHostID):
		body["targetHostId"] = targetHostID
	default:
		return out, errors.New("invalid wake target")
	}
	err := c.call(ctx, "POST", "/api/hosts/"+url.PathEscape(host)+"/wake", body, &out)
	return out, err
}

// PendingWakes lists wake requests for Macs on this host's network.
func (c *Client) PendingWakes(ctx context.Context, host string) ([]WakeRequest, error) {
	if !ValidID(host) {
		return nil, errors.New("invalid host id")
	}
	var out struct {
		Requests []WakeRequest `json:"requests"`
	}
	err := c.call(ctx, "GET", "/api/hosts/"+url.PathEscape(host)+"/wake-requests", nil, &out)
	return out.Requests, err
}

// WakeSent records that this host broadcast the request.
func (c *Client) WakeSent(ctx context.Context, host, requestID string) error {
	if !ValidID(host) || !ValidID(requestID) {
		return errors.New("invalid wake request")
	}
	var out struct {
		OK bool `json:"ok"`
	}
	return c.call(ctx, "POST", "/api/hosts/"+url.PathEscape(host)+"/wake-requests/"+url.PathEscape(requestID)+"/sent", nil, &out)
}
