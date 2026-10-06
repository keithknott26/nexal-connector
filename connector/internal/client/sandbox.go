package client

import (
	"context"
	"encoding/json"
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
	n.Managed = false // local only: the coordinator knows which hosts are managed and refuses unknown keys
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

// ListSandboxes is GET /api/v2/hosts/:hostId/sandboxes: the host-token twin of the
// member list (the CLI holds only the host credential). The body is returned exactly
// as the coordinator sent it (a JSON object, {"sandboxes":[...]}), so the Mac app sees
// every field the coordinator adds.
func (c *Client) ListSandboxes(ctx context.Context, hostID string) (json.RawMessage, error) {
	p, err := sandboxPath(hostID, "sandboxes")
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := c.callLenient(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ConnectSandbox is POST /api/v2/hosts/:hostId/sandboxes/:id/connect for kind
// ssh|vnc|files; the coordinator acts for the host's owner. publicKey (an ssh-ed25519
// key) is required for ssh and files. The response carries one-time secrets
// (certificate, VNC password): it is returned unchanged and never logged here.
func (c *Client) ConnectSandbox(ctx context.Context, hostID, id, kind, publicKey string) (json.RawMessage, error) {
	if !ValidID(id) {
		return nil, errors.New("invalid sandbox id")
	}
	p, err := sandboxPath(hostID, "sandboxes/"+url.PathEscape(id)+"/connect")
	if err != nil {
		return nil, err
	}
	body := map[string]string{"kind": kind}
	if publicKey != "" {
		body["publicKey"] = publicKey
	}
	var out json.RawMessage
	if err := c.callLenient(ctx, "POST", p, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateSandbox is POST /api/v2/hosts/:hostId/sandboxes: start a VM or dev container
// as the host's owner. body is the member create request (imageId, runnerHostId, kind,
// size, lifecycle, lifetimeHours, devcontainer, ...), passed through unchanged.
func (c *Client) CreateSandbox(ctx context.Context, hostID string, body json.RawMessage) (json.RawMessage, error) {
	p, err := sandboxPath(hostID, "sandboxes")
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("invalid create request")
	}
	var out json.RawMessage
	if err := c.callLenient(ctx, "POST", p, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SandboxImages is GET /api/v2/hosts/:hostId/sandbox-images[?runner=]: the image
// catalog for a runner computer (this host when runner is empty).
// SandboxRunners is GET /api/v2/hosts/:id/sandbox-runners: where the owner can create an
// instance (this computer, other computers that host instances, neXal storage).
func (c *Client) SandboxRunners(ctx context.Context, hostID string) (json.RawMessage, error) {
	p, err := sandboxPath(hostID, "sandbox-runners")
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := c.callLenient(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SandboxImages(ctx context.Context, hostID, runner string) (json.RawMessage, error) {
	p, err := sandboxPath(hostID, "sandbox-images")
	if err != nil {
		return nil, err
	}
	if runner != "" {
		if !ValidID(runner) {
			return nil, errors.New("invalid runner id")
		}
		p += "?runner=" + url.QueryEscape(runner)
	}
	var out json.RawMessage
	if err := c.callLenient(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

var (
	_ sandbox.Coordinator          = (*Client)(nil)
	_ sandbox.HostingPublisher     = (*Client)(nil)
	_ sandbox.HostingStateReporter = (*Client)(nil)
)
