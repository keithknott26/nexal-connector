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
