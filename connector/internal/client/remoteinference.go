package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"nexal/connector/internal/remoteinference"
)

// Remote AI inference routes (coordinator src/remote-inference.ts), host-token authenticated.

func remoteInferencePath(hostID, leaf string) (string, error) {
	if !ValidID(hostID) {
		return "", errors.New("invalid host id")
	}
	return "/api/v2/hosts/" + url.PathEscape(hostID) + "/" + leaf, nil
}

// RemoteInferenceTasks is GET .../remote-inference?role=target: requests waiting for this Mac.
func (c *Client) RemoteInferenceTasks(ctx context.Context, hostID string) ([]remoteinference.Task, error) {
	p, err := remoteInferencePath(hostID, "remote-inference?role=target")
	if err != nil {
		return nil, err
	}
	var out struct {
		Tasks []remoteinference.Task `json:"tasks"`
	}
	if err := c.callLenient(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

// AllRemoteInference is GET .../remote-inference: every recent request in the organization.
func (c *Client) AllRemoteInference(ctx context.Context, hostID string) ([]remoteinference.Task, error) {
	p, err := remoteInferencePath(hostID, "remote-inference")
	if err != nil {
		return nil, err
	}
	var out struct {
		Tasks []remoteinference.Task `json:"tasks"`
	}
	if err := c.callLenient(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

// CreateRemoteInference asks targetHostID to serve modelID.
func (c *Client) CreateRemoteInference(ctx context.Context, hostID, targetHostID, modelID string) (*remoteinference.Task, error) {
	p, err := remoteInferencePath(hostID, "remote-inference")
	if err != nil {
		return nil, err
	}
	var out remoteinference.Task
	if err := c.callLenient(ctx, "POST", p, map[string]string{"targetHostId": targetHostID, "modelId": modelID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateRemoteInferencePool asks several Macs to serve modelID together; the first one answers.
func (c *Client) CreateRemoteInferencePool(ctx context.Context, hostID string, targetHostIDs []string, modelID string) ([]remoteinference.Task, error) {
	p, err := remoteInferencePath(hostID, "remote-inference")
	if err != nil {
		return nil, err
	}
	var out struct {
		Tasks []remoteinference.Task `json:"tasks"`
	}
	if err := c.callLenient(ctx, "POST", p, map[string]any{"targetHostIds": targetHostIDs, "modelId": modelID}, &out); err != nil {
		return nil, err
	}
	if len(out.Tasks) == 0 {
		return nil, errors.New("the coordinator did not return the pool")
	}
	return out.Tasks, nil
}

// ReportRemoteInference is POST .../remote-inference-state (the target Mac only).
func (c *Client) ReportRemoteInference(ctx context.Context, hostID string, r remoteinference.Report) error {
	p, err := remoteInferencePath(hostID, "remote-inference-state")
	if err != nil {
		return err
	}
	err = c.callLenient(ctx, "POST", p, r, nil)
	// A coordinator older than the progress column refuses the unknown key (400 invalid_schema) and
	// would drop every report that carries it: resend without it so the step still shows.
	var se *StatusError
	if r.Progress != nil && errors.As(err, &se) && se.Status == http.StatusBadRequest {
		r.Progress = nil
		err = c.callLenient(ctx, "POST", p, r, nil)
	}
	return err
}

// CancelRemoteInference stops a request (and the server it started).
func (c *Client) CancelRemoteInference(ctx context.Context, hostID, taskID string) error {
	if !ValidID(taskID) {
		return errors.New("invalid task id")
	}
	p, err := remoteInferencePath(hostID, "remote-inference/"+url.PathEscape(taskID)+"/cancel")
	if err != nil {
		return err
	}
	return c.callLenient(ctx, "POST", p, nil, nil)
}

// InferenceGatewayState is the coordinator's answer to InferenceGateway.
type InferenceGatewayState struct {
	Open      bool   `json:"open"`
	State     string `json:"state,omitempty"` // applied | pending | failed
	Error     string `json:"error,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Ports     string `json:"ports,omitempty"` // "8001-8099"
}

// InferenceGateway is PUT .../inference-gateway. While this Mac serves an AI model, open=true starts or renews a
// short lease that lets the neXal storage host connect to it on TCP 8001-8099; open=false ends it. A lease that
// is not renewed runs out by itself, so a crashed or sleeping Mac does not leave the ports open.
func (c *Client) InferenceGateway(ctx context.Context, hostID string, open bool) (*InferenceGatewayState, error) {
	p, err := remoteInferencePath(hostID, "inference-gateway")
	if err != nil {
		return nil, err
	}
	var out InferenceGatewayState
	if err := c.call(ctx, "PUT", p, map[string]bool{"open": open}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
