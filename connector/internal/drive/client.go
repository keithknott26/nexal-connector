package drive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"nexal/connector/internal/config"
)

// maxDriveKeyLength mirrors MAX_DRIVE_KEY_LENGTH in the coordinator.
const maxDriveKeyLength = 1024

// ValidateKey applies the coordinator's drive-key rule on this side of the wire.
//
// The rule is duplicated here on purpose. The coordinator is the authority and
// rejects a bad key regardless, but a client that uploads 25 MiB before being
// told the name was invalid wastes the user's uplink, and -- more importantly --
// the key is the AEAD associated data, so a key this client would treat as valid
// and the server would not is a disagreement that must be caught before
// anything is encrypted under it.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return errors.New("drive: key must not be empty")
	case len(key) > maxDriveKeyLength:
		return fmt.Errorf("drive: key must be at most %d bytes", maxDriveKeyLength)
	case strings.HasPrefix(key, "/"), strings.HasSuffix(key, "/"):
		return errors.New("drive: key must not begin or end with '/'")
	case strings.Contains(key, "//"):
		return errors.New("drive: key must not contain an empty path segment")
	case strings.Contains(key, `\`):
		return errors.New(`drive: key must not contain '\'`)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "." || segment == ".." {
			return errors.New("drive: key must not contain a '.' or '..' segment")
		}
	}
	// Control characters would survive the checks above but make a key that
	// cannot be typed back, and that renders ambiguously in any log or UI.
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return errors.New("drive: key must not contain control characters")
		}
	}
	return nil
}

// Client talks to the coordinator's drive routes with a host credential.
//
// It is separate from internal/client.Client because that type's `call` helper
// is JSON in, JSON out, with a 64 KiB response cap -- correct for control-plane
// calls and wrong for object bodies, which are binary and up to 25 MiB. Reusing
// it would have meant loosening that cap for every control-plane call.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient mirrors internal/client.New's transport hardening: no ambient proxy
// (which would forward the host credential), TLS 1.3 floor, nil
// CurvePreferences so Go negotiates X25519MLKEM768, and redirects refused.
//
// The timeout is per-request and far longer than the control plane's 10s,
// because a 25 MiB body on a domestic uplink legitimately takes minutes.
func NewClient(base, token string, dev bool) (*Client, error) {
	if err := config.ValidateURL(base, dev); err != nil {
		return nil, err
	}
	if token == "" || len(token) > 4096 {
		return nil, errors.New("drive: invalid host credential")
	}
	for _, r := range token {
		if r <= 32 || r > 126 {
			return nil, errors.New("drive: invalid host credential")
		}
	}
	tr := &http.Transport{
		Proxy:       nil,
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		// Nil CurvePreferences is load-bearing, not an omission: setting it to
		// "pin" a group disables every group not listed and would ship a
		// classical-only handshake. See internal/client.New for the full note.
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS13},
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http: &http.Client{
			Timeout:       10 * time.Minute,
			Transport:     tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("drive: coordinator redirect refused") },
		},
	}, nil
}

// StatusError is a non-2xx response carrying the status and the coordinator's
// error code, and nothing else -- no URL, no bearer, no raw body. The code is
// what lets a caller tell "you are over your quota" from "that object is not
// here" without matching on prose.
type StatusError struct {
	Status  int
	Code    string
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("coordinator refused the request (HTTP %d, %s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("coordinator refused the request (HTTP %d)", e.Status)
}

// statusError decodes the coordinator's {"error":{"code","message"}} shape.
// A body that is not that shape yields a StatusError with just the status,
// because echoing an unrecognised body would put server-controlled text into
// the user's terminal.
func statusError(resp *http.Response) error {
	e := &StatusError{Status: resp.StatusCode}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return e
	}
	var decoded struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return e
	}
	e.Code = sanitize(decoded.Error.Code, 64)
	e.Message = sanitize(decoded.Error.Message, 400)
	return e
}

// sanitize strips control characters and bounds length, so a hostile or broken
// coordinator cannot rewrite the terminal with escape sequences or flood it.
func sanitize(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len() >= limit {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, errors.New("drive: cannot construct request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

// Put uploads a sealed body under key.
//
// The SHA-256 header is the digest of the CIPHERTEXT, because that is what the
// coordinator receives and verifies. The coordinator refuses a mismatch, which
// makes this an end-to-end integrity check across the whole path rather than a
// claim the client makes about itself.
func (c *Client) Put(ctx context.Context, key string, sealed []byte) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}
	if len(sealed) > MaxObjectBytes {
		return Object{}, fmt.Errorf("%w: sealed object is %d bytes, over the %d-byte limit", ErrPlaintextTooLarge, len(sealed), MaxObjectBytes)
	}
	sum := sha256.Sum256(sealed)
	req, err := c.request(ctx, http.MethodPut, "/api/drive/objects/"+escapeKey(key), bytes.NewReader(sealed))
	if err != nil {
		return Object{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Nexal-Content-Sha256", hex.EncodeToString(sum[:]))
	req.ContentLength = int64(len(sealed))
	resp, err := c.http.Do(req)
	if err != nil {
		return Object{}, transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Object{}, statusError(resp)
	}
	var out struct {
		Object Object `json:"object"`
	}
	if err := decodeJSON(resp.Body, &out); err != nil {
		return Object{}, err
	}
	return out.Object, nil
}

// Get downloads the sealed body stored under key. The caller decrypts it; this
// method deliberately does not, so that a caller verifying storage can fetch
// bytes without holding a key.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	req, err := c.request(ctx, http.MethodGet, "/api/drive/objects/"+escapeKey(key), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	// Bounded by the per-object limit plus one byte: a body larger than any
	// object this coordinator can store is a broken or hostile response, and
	// reading it into memory unbounded is how a client gets killed by the OOM
	// killer instead of returning an error.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxObjectBytes+1))
	if err != nil {
		return nil, transportError(err)
	}
	if len(body) > MaxObjectBytes {
		return nil, errors.New("drive: coordinator returned more bytes than any object may contain")
	}
	return body, nil
}

// Usage is the tenant's drive accounting.
type Usage struct {
	Tier         string `json:"tier"`
	CeilingBytes int64  `json:"ceilingBytes"`
	UsedBytes    int64  `json:"usedBytes"`
	ObjectCount  int64  `json:"objectCount"`
	UpdatedAt    string `json:"updatedAt"`
}

// Object is one ledger entry.
type Object struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	SizeBytes int64  `json:"sizeBytes"`
	CreatedAt string `json:"createdAt"`
}

func (c *Client) Usage(ctx context.Context) (Usage, error) {
	var out Usage
	err := c.getJSON(ctx, "/api/drive/usage", &out)
	return out, err
}

// List returns one page of objects. The cursor is opaque and comes from a
// previous response; it is never constructed here.
func (c *Client) List(ctx context.Context, cursor string, limit int) ([]Object, string, error) {
	path := "/api/drive/objects"
	sep := "?"
	if limit > 0 {
		path += sep + "limit=" + fmt.Sprint(limit)
		sep = "&"
	}
	if cursor != "" {
		path += sep + "cursor=" + escapeQuery(cursor)
	}
	var out struct {
		Objects    []Object `json:"objects"`
		NextCursor string   `json:"nextCursor"`
	}
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, "", err
	}
	return out.Objects, out.NextCursor, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	return decodeJSON(resp.Body, out)
}

// decodeJSON reads a bounded control-plane response. Unknown fields are
// tolerated here, unlike internal/client's DisallowUnknownFields: this client
// reads a subset of the drive view on purpose, and a coordinator that adds a
// field should not break every connector in the field.
func decodeJSON(r io.Reader, out any) error {
	body, err := io.ReadAll(io.LimitReader(r, 256<<10))
	if err != nil {
		return transportError(err)
	}
	if err := config.CheckJSONObject(body); err != nil {
		return errors.New("drive: invalid coordinator response")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("drive: invalid coordinator response schema")
	}
	return nil
}

// transportError keeps the URL and the bearer token out of the message. net
// errors routinely embed the full request URL, which would put the credential's
// destination -- and with some proxies the credential itself -- into logs.
func transportError(err error) error {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("drive: the coordinator did not respond in time")
	}
	return errors.New("drive: cannot reach the coordinator")
}

// escapeKey percent-encodes a drive key for use in a path.
//
// url.PathEscape is not usable: it leaves '/' alone, which is correct for a
// single segment and wrong here, because the coordinator's route captures the
// whole remainder of the path as ONE key. A key containing '/' is legal and
// common ("backups/2026/09/mac-mini.sparsebundle"), so the separators must
// survive while everything that could change the route's meaning must not.
func escapeKey(key string) string {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c == '/',
			c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			// Byte-wise, not rune-wise: percent-encoding is defined on octets, and
			// encoding a multi-byte rune as a single %XX would corrupt it.
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// escapeQuery encodes an opaque cursor for a query parameter. Unlike a key, '/'
// here has no structural meaning and is escaped with everything else.
func escapeQuery(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
