package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"nexal/connector/internal/privateruntime"
	"strconv"
)

func runtimePath(host, session string) (string, error) {
	if !ValidID(host) || (session != "" && !ValidID(session)) {
		return "", errors.New("invalid runtime identity")
	}
	path := "/api/hosts/" + host + "/inference-runtime"
	if session != "" {
		path += "/" + session
	}
	return path, nil
}
func (c *Client) OpenRuntime(ctx context.Context, host string) (privateruntime.Session, error) {
	var out privateruntime.Session
	path, err := runtimePath(host, "")
	if err == nil {
		err = c.call(ctx, "POST", path, nil, &out)
	}
	return out, err
}
func (c *Client) RenewRuntime(ctx context.Context, host, session string) (privateruntime.Lease, error) {
	var out privateruntime.Lease
	path, err := runtimePath(host, session)
	if err == nil {
		err = c.call(ctx, "POST", path, map[string]any{}, &out)
	}
	return out, err
}
func (c *Client) CloseRuntime(ctx context.Context, host, session string) error {
	path, err := runtimePath(host, session)
	if err != nil {
		return err
	}
	var out struct {
		OK bool `json:"ok"`
	}
	return c.call(ctx, "DELETE", path, nil, &out)
}
func (c *Client) DownloadRuntime(ctx context.Context, host, session string, index int, dest io.Writer, size int64) error {
	path, err := runtimePath(host, session)
	if err != nil || index < 0 || index > 127 || size < 1 || size > 16<<30 {
		return errors.New("invalid runtime artifact")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+path+"/files/"+strconv.Itoa(index), nil)
	if err != nil {
		return errors.New("invalid runtime request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/octet-stream")
	// Retain fixed-origin TLS/no-proxy/no-redirect policy; large shards use the
	// session-owned context rather than the JSON client's 10-second total timeout.
	transport := *c.http
	transport.Timeout = 0
	response, err := transport.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return &StatusError{Status: response.StatusCode}
	}
	if response.ContentLength != size {
		return errors.New("runtime artifact length mismatch")
	}
	count, err := io.Copy(dest, io.LimitReader(response.Body, size+1))
	if err != nil {
		return transportError(err)
	}
	if count != size {
		return errors.New("runtime artifact length mismatch")
	}
	return nil
}
