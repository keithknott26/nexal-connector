package client

import (
	"context"
	"errors"
	"net/url"

	"nexal/connector/internal/sandbox"
)

// Throwaway-host runner routes (docs/sandboxes/API.md, "Runner routes"). They
// authenticate with the host bearer token like every other host route, use the
// lenient decoder (the coordinator deploys before installed connectors update),
// and answer 404/503 from an older coordinator, which IsNotSupported names. *Client
// satisfies sandbox.Coordinator, sandbox.HostingPublisher and
// sandbox.HostingStateReporter.

func sandboxPath(hostID, leaf string) (string, error) {
	if !ValidID(hostID) {
		return "", errors.New("invalid host id")
	}
	return "/api/v2/hosts/" + url.PathEscape(hostID) + "/" + leaf, nil
}

// SandboxTasks is GET /api/v2/hosts/:id/sandbox-tasks. Tasks carry secrets (mesh
// key, VNC password, drive token): they are returned to the caller only and never
// logged here.
func (c *Client) SandboxTasks(ctx context.Context, hostID string) ([]sandbox.Task, error) {
	p, err := sandboxPath(hostID, "sandbox-tasks")
	if err != nil {
		return nil, err
	}
	var out struct {
		Tasks []sandbox.Task `json:"tasks"`
	}
	if err := c.callLenient(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

// ReportSandboxState is POST /api/v2/hosts/:id/sandbox-state.
func (c *Client) ReportSandboxState(ctx context.Context, hostID string, r sandbox.StateReport) error {
	p, err := sandboxPath(hostID, "sandbox-state")
	if err != nil {
		return err
	}
	if !ValidID(r.SandboxID) {
		return errors.New("invalid sandbox id")
	}
	return c.callLenient(ctx, "POST", p, r, nil)
}

// PutSandboxHosting is PUT /api/v2/hosts/:id/sandbox-hosting: the Mac's opt-in
// and caps, as chosen in the Mac app.
func (c *Client) PutSandboxHosting(ctx context.Context, hostID string, h sandbox.HostingConfig) error {
	p, err := sandboxPath(hostID, "sandbox-hosting")
	if err != nil {
		return err
	}
	n, err := h.Normalize()
	if err != nil {
		return err
	}
	return c.callLenient(ctx, "PUT", p, n, nil)
}

// ReportSandboxHostingState is POST /api/v2/hosts/:id/sandbox-hosting-state,
// called on sleep and wake. A runner that reported awake:false is "paused", not
// "failed".
func (c *Client) ReportSandboxHostingState(ctx context.Context, hostID string, awake, onBattery bool) error {
	p, err := sandboxPath(hostID, "sandbox-hosting-state")
	if err != nil {
		return err
	}
	in := struct {
		Awake     bool `json:"awake"`
		OnBattery bool `json:"onBattery"`
	}{awake, onBattery}
	return c.callLenient(ctx, "POST", p, in, nil)
}

var (
	_ sandbox.Coordinator          = (*Client)(nil)
	_ sandbox.HostingPublisher     = (*Client)(nil)
	_ sandbox.HostingStateReporter = (*Client)(nil)
)
