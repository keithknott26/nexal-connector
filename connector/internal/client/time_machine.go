package client

import (
	"context"
	"errors"
	"net/url"
	"time"

	"nexal/connector/internal/timemachine"
)

func (c *Client) TimeMachineConfig(ctx context.Context, hostID string) (timemachine.Config, error) {
	var wire timemachine.CoordinatorConfig
	if !ValidID(hostID) {
		return timemachine.Config{}, errors.New("invalid host id")
	}
	if err := c.call(ctx, "GET", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/config", nil, &wire); err != nil {
		return timemachine.Config{}, err
	}
	return wire.Local()
}

func (c *Client) TimeMachineCredentials(ctx context.Context, hostID string) (timemachine.Credentials, error) {
	var out timemachine.Credentials
	if !ValidID(hostID) {
		return out, errors.New("invalid host id")
	}
	if err := c.call(ctx, "POST", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/credentials", struct{}{}, &out); err != nil {
		return timemachine.Credentials{}, err
	}
	if err := out.Validate(time.Now()); err != nil {
		out.Zero()
		return timemachine.Credentials{}, err
	}
	return out, nil
}

func (c *Client) ReportTimeMachineStatus(ctx context.Context, hostID string, status timemachine.Status) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	state, code := timemachine.CoordinatorState(status)
	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.call(ctx, "POST", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/status", struct {
		State     string `json:"state"`
		ErrorCode string `json:"errorCode,omitempty"`
	}{state, code}, &out); err != nil {
		return err
	}
	if !out.Accepted {
		return errors.New("coordinator did not accept Time Machine status")
	}
	return nil
}

func (c *Client) ReportTimeMachineUsage(ctx context.Context, hostID string, sample timemachine.Usage) error {
	if !ValidID(hostID) {
		return errors.New("invalid host id")
	}
	if err := sample.Validate(); err != nil {
		return err
	}
	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.call(ctx, "POST", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/usage", sample, &out); err != nil {
		return err
	}
	if !out.Accepted {
		return errors.New("coordinator did not accept Time Machine usage")
	}
	return nil
}
