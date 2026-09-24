package client

import (
	"context"
	"errors"
	"net/url"

	"nexal/connector/internal/timemachine"
)

// TimeMachineConfig reads non-secret desired state. A 404/503 is returned as a
// typed StatusError; callers must treat that as disabled, never reuse a stale
// enabled document indefinitely.
func (c *Client) TimeMachineConfig(ctx context.Context, hostID string) (timemachine.Config, error) {
	var out timemachine.Config
	if !ValidID(hostID) {
		return out, errors.New("invalid host id")
	}
	err := c.call(ctx, "GET", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/config", nil, &out)
	if err == nil {
		err = out.Validate()
	}
	return out, err
}

func (c *Client) ReportTimeMachineStatus(ctx context.Context, hostID string, status timemachine.Status) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.call(ctx, "POST", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/status", status, &out); err != nil {
		return err
	}
	if !out.OK {
		return errors.New("coordinator did not accept Time Machine status")
	}
	return nil
}
