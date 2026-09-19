// Package client implements the contract's host-scoped REST prototype.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nexal/connector/internal/config"
)

type Attempt struct {
	ID             string    `json:"id"`
	JobID          string    `json:"jobId"`
	HostID         string    `json:"hostId"`
	Template       string    `json:"template"`
	Samples        int64     `json:"samples"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
	MaxCostCents   int64     `json:"maxCostCents"`
}
type Result struct {
	Samples int64   `json:"samples"`
	Inside  int64   `json:"inside"`
	Pi      float64 `json:"pi"`
}
type PQ struct {
	Configured bool   `json:"configured"`
	Verified   bool   `json:"verified"`
	Protocol   string `json:"protocol"`
}
type Heartbeat struct {
	OwnerActive          bool   `json:"ownerActive"`
	AvailableMemoryBytes uint64 `json:"availableMemoryBytes"`
	PQ                   PQ     `json:"pq"`
	Version              string `json:"version"`
}
type Renewal struct {
	OK              bool      `json:"ok"`
	LeaseExpiresAt  time.Time `json:"leaseExpiresAt"`
	CancelRequested bool      `json:"cancelRequested"`
}
type Enrollment struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Arch         string `json:"arch"`
	CPUCores     int    `json:"cpuCores"`
	MemoryBytes  uint64 `json:"memoryBytes"`
	StorageBytes uint64 `json:"storageBytes"`
}
type Identity struct {
	HostID string `json:"hostId"`
	Token  string `json:"token"`
}
type API interface {
	Heartbeat(context.Context, string, Heartbeat) error
	Next(context.Context, string) (*Attempt, error)
	Renew(context.Context, string) (Renewal, error)
	Complete(context.Context, string, Result, float64) (bool, error)
}
type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(base, token string, dev bool) (*Client, error) {
	if err := config.ValidateURL(base, dev); err != nil {
		return nil, err
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid host credential")
	}
	tr := &http.Transport{
		Proxy:                 nil, // avoid ambient proxy settings forwarding host credentials
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{
		Timeout: 10 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("coordinator redirects are forbidden") },
	}}, nil
}
func ValidID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return errors.New("cannot encode request")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return errors.New("cannot construct request")
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("coordinator request failed")
	} // never expose URL or bearer in errors
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("coordinator rejected request (HTTP %d)", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return errors.New("coordinator response too large or unreadable")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return errors.New("invalid coordinator response schema")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing coordinator response")
	}
	return nil
}
func (c *Client) Enroll(ctx context.Context, e Enrollment) (Identity, error) {
	var out Identity
	err := c.call(ctx, "POST", "/api/hosts/enroll", e, &out)
	if err == nil && (!ValidID(out.HostID) || len(out.Token) < 16 || len(out.Token) > 4096 || strings.ContainsAny(out.Token, " \r\n\t")) {
		err = errors.New("invalid enrollment identity")
	}
	return out, err
}
func (c *Client) Heartbeat(ctx context.Context, host string, h Heartbeat) error {
	if !ValidID(host) {
		return errors.New("invalid host id")
	}
	var out struct {
		OK           bool `json:"ok"`
		LeaseSeconds int  `json:"leaseSeconds"`
	}
	if err := c.call(ctx, "POST", "/api/hosts/"+url.PathEscape(host)+"/heartbeat", h, &out); err != nil {
		return err
	}
	if !out.OK {
		return errors.New("heartbeat not accepted")
	}
	return nil
}
func (c *Client) Next(ctx context.Context, host string) (*Attempt, error) {
	if !ValidID(host) {
		return nil, errors.New("invalid host id")
	}
	var out struct {
		Attempt *Attempt `json:"attempt"`
	}
	err := c.call(ctx, "GET", "/api/hosts/"+url.PathEscape(host)+"/next", nil, &out)
	return out.Attempt, err
}
func (c *Client) Renew(ctx context.Context, id string) (Renewal, error) {
	var out Renewal
	if !ValidID(id) {
		return out, errors.New("invalid attempt id")
	}
	err := c.call(ctx, "POST", "/api/attempts/"+url.PathEscape(id)+"/heartbeat", struct{}{}, &out)
	if err == nil && !out.OK {
		err = errors.New("lease renewal rejected")
	}
	return out, err
}
func (c *Client) Complete(ctx context.Context, id string, r Result, seconds float64) (bool, error) {
	if !ValidID(id) {
		return false, errors.New("invalid attempt id")
	}
	var out struct {
		Accepted bool `json:"accepted"`
	}
	err := c.call(ctx, "POST", "/api/attempts/"+url.PathEscape(id)+"/complete",
		struct {
			Result       Result  `json:"result"`
			UsageSeconds float64 `json:"usageSeconds"`
		}{r, seconds}, &out)
	return out.Accepted, err
}
