package client

import (
	"bytes"
	"context"
	"encoding/json"
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

// TimeMachineClientConfig fetches the config document and interprets it as
// gateway-client mode. The legacy connector-hosted document has no role field,
// so it is probed loosely here and decoded strictly only when role is "client".
func (c *Client) TimeMachineClientConfig(ctx context.Context, hostID string) (timemachine.ClientConfig, error) {
	var out timemachine.ClientConfig
	if !ValidID(hostID) {
		return out, errors.New("invalid host id")
	}
	var probe map[string]json.RawMessage
	if err := c.call(ctx, "GET", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/config", nil, &probe); err != nil {
		return out, err
	}
	var role string
	if raw, ok := probe["role"]; !ok || json.Unmarshal(raw, &role) != nil || role != timemachine.RoleClient {
		return out, nil // legacy/server document: Role stays empty
	}
	whole, err := json.Marshal(probe)
	if err != nil {
		return out, err
	}
	d := json.NewDecoder(bytes.NewReader(whole))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return timemachine.ClientConfig{}, errors.New("invalid coordinator response schema")
	}
	return out, nil
}

// TimeMachineSMBCredential returns the gateway SMB credential for this computer.
func (c *Client) TimeMachineSMBCredential(ctx context.Context, hostID string, expected timemachine.Destination) (timemachine.SMBCredential, error) {
	var out timemachine.SMBCredential
	if !ValidID(hostID) {
		return out, errors.New("invalid host id")
	}
	if err := c.call(ctx, "POST", "/api/v2/devices/"+url.PathEscape(hostID)+"/time-machine/credentials", struct{}{}, &out); err != nil {
		return timemachine.SMBCredential{}, err
	}
	if err := out.Validate(expected); err != nil {
		out.Zero()
		return timemachine.SMBCredential{}, err
	}
	return out, nil
}
