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
	Execution      string    `json:"execution,omitempty"`
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
	AcceptJobsUntil      string `json:"acceptJobsUntil,omitempty"`
	OwnerActive          bool   `json:"ownerActive"`
	AvailableMemoryBytes uint64 `json:"availableMemoryBytes"`
	PQ                   PQ     `json:"pq"`
	Version              string `json:"version"`
	// Wake lets the coordinator relay Wake-on-LAN requests for this host.
	Wake *WakeInfo `json:"wake,omitempty"`
}

// WakeInfo is the interface another Mac needs to wake this one, plus this
// host's own tunnel address so peers that know it only by that can name it.
type WakeInfo struct {
	MAC       string `json:"mac,omitempty"`
	Broadcast string `json:"broadcast,omitempty"`
	Tunnel    string `json:"tunnel,omitempty"`
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
	// dev records that this client was built for a development profile, where the
	// coordinator may be plain-HTTP loopback. It is read only by the pairing
	// validator, to relax the https clause that would otherwise make the loopback
	// profile (and the offline test harness) unable to construct a payload at all.
	dev bool
}

func New(base, token string, dev bool) (*Client, error) {
	if err := config.ValidateURL(base, dev); err != nil {
		return nil, err
	}
	if len(token) > 4096 {
		return nil, errors.New("invalid host credential")
	}
	for _, r := range token {
		if r <= 32 || r > 126 {
			return nil, errors.New("invalid host credential")
		}
	}
	tr := &http.Transport{
		Proxy:       nil, // avoid ambient proxy settings forwarding host credentials
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		// TLS 1.3 floor: the coordinator is our own origin, so there is no 1.2-only
		// peer to accommodate, and the hybrid ML-KEM key exchanges are 1.3-only.
		//
		// CurvePreferences is deliberately left nil, and must stay nil. Go enables
		// X25519MLKEM768 (and, from Go 1.26, the SecP hybrids) only for a nil
		// CurvePreferences; setting the field to "pin" a group disables every group
		// not listed, which is how a change meant as post-quantum hardening ends up
		// shipping a classical-only handshake. Go also ignores the list's order, so
		// listing a group first buys nothing. Leaving it nil already negotiates
		// X25519MLKEM768 against the coordinator, and keeps a working fallback if an
		// origin ever drops the group instead of failing every request.
		//
		// The GODEBUG defaults that gate these groups (tlsmlkem, tlssecpmlkem) come
		// from the go directive in go.mod, not from the installed toolchain: lowering
		// that directive below go 1.24 turns post-quantum key agreement off with no
		// code change here. TestCoordinatorHandshakeIsTLS13AndPostQuantum asserts the
		// negotiated group, so that downgrade fails the test suite instead of passing
		// silently. Certificate authentication remains classical (WebPKI, no ML-DSA).
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS13}, // gitleaks:allow -- public algorithm names in the comment above, not key material
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, dev: dev, http: &http.Client{
		Timeout: 10 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectForbidden },
	}}, nil
}

// StatusError is a non-2xx coordinator response. It deliberately carries the
// status code and NOTHING else: no URL, no response body, no bearer token.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("coordinator rejected request (HTTP %d)", e.Status)
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
		return transportError(err)
	} // never expose URL or bearer in errors
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if path == "/api/hosts/enroll" && resp.StatusCode == http.StatusConflict {
			return errors.New("coordinator rejected enrollment (HTTP 409): invitation invalid, expired, or already used; generate a fresh enr_ invitation in Hosts > Enroll host, not an owner token")
		}
		// Typed so a caller can distinguish "the feature is switched off" (503) from
		// "this host credential is not accepted" (401) without string matching, which
		// is what the pairing command needs in order to tell the owner WHICH of the
		// several possible causes applies (HARDENING-PLAN §26). The message is
		// unchanged, so existing callers and their tests see exactly what they did.
		return &StatusError{Status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil {
		return transportError(err)
	}
	if len(b) > 64<<10 {
		return errors.New("coordinator response too large or unreadable")
	}
	if err := config.CheckJSONObject(b); err != nil {
		return errors.New("invalid coordinator response schema")
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
		OK               bool `json:"ok"`
		LeaseSeconds     int  `json:"leaseSeconds"`
		HeartbeatSeconds int  `json:"heartbeatSeconds"`
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
