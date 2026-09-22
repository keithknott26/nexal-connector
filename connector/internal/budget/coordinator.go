package budget

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nexal/connector/internal/config"
)

// The persistence transport for §16, deliberately its own ~100 lines rather than a method
// bolted onto internal/client.
//
// WHY SEPARATE. internal/client is the job/attempt contract and is owned elsewhere; this
// is a different concern with a different failure posture. More importantly, the budget's
// correctness rests on the transport doing exactly two things — never retrying with a NEW
// report id, and never treating an ambiguous failure as success — and that is easier to
// guarantee in a file this small than to maintain as an invariant inside a general client.
//
// FAILURE POSTURE. Every error here ends in the caller keeping its pending report id and
// continuing to enforce locally, so there is nothing to gain from retrying inside this
// transport. A 4xx is not retried at all: a rejected report is a contract bug, and
// hammering it would spend the donor's uplink on nothing.

const (
	// requestTimeout bounds a single call. Short, because a hung report must degrade to
	// the offline floor rather than pin a goroutine: being slow to learn the remaining
	// budget is safe (the floor is already enforced), being slow to give up is not.
	requestTimeout = 15 * time.Second
	// maxResponseBytes caps what is parsed from the coordinator. The window payload is a
	// few hundred bytes; anything larger is a misrouted response or a hostile endpoint,
	// and an unbounded read here would be a memory exhaustion path on the donor's machine.
	maxResponseBytes = 64 * 1024
)

// HTTPCoordinator talks to the coordinator's host-scoped bandwidth routes using the
// host's own enrollment token.
type HTTPCoordinator struct {
	base   string
	hostID string
	token  string
	client *http.Client
}

// NewHTTPCoordinator validates the origin with the same helper the rest of the connector
// uses, so a budget endpoint cannot be pointed somewhere the job client would refuse
// (plaintext HTTP off-loopback, credentials in the URL, and so on).
func NewHTTPCoordinator(base, hostID, token string, dev bool) (*HTTPCoordinator, error) {
	if err := config.ValidateURL(base, dev); err != nil {
		return nil, err
	}
	if hostID == "" || token == "" {
		return nil, errors.New("budget coordinator requires a host id and token")
	}
	// Path-segment safety: the host id is interpolated into a URL, so anything that could
	// escape the segment is refused rather than escaped, because a host id that needs
	// escaping is not a host id this connector was enrolled with.
	if url.PathEscape(hostID) != hostID {
		return nil, errors.New("host id is not a safe URL path segment")
	}
	return &HTTPCoordinator{
		base: strings.TrimSuffix(base, "/"), hostID: hostID, token: token,
		client: &http.Client{
			Timeout: requestTimeout,
			// TLS 1.3 minimum, matching the connector's other outbound client. A budget
			// report carries a host token, so it gets the same floor as the job API.
			Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}},
		},
	}, nil
}

// wireWindow is the subset of the coordinator's payload this connector depends on. Unknown
// fields are IGNORED on purpose (no DisallowUnknownFields): the coordinator publishes a
// large, growing view and a connector build that refused to parse a newer one would stop
// enforcing the real budget the moment the Worker shipped a new field.
type wireWindow struct {
	LimitBytes                 uint64 `json:"limitBytes"`
	UsedBytes                  uint64 `json:"usedBytes"`
	RemainingBytes             uint64 `json:"remainingBytes"`
	SustainedBytesPerSecond    uint64 `json:"sustainedBytesPerSecond"`
	OfflineFloorBytesPerSecond uint64 `json:"offlineFloorBytesPerSecond"`
	Exhausted                  bool   `json:"exhausted"`
	Window                     struct {
		Days int `json:"days"`
	} `json:"window"`
}

func (c *HTTPCoordinator) Window(ctx context.Context) (Window, error) {
	return c.call(ctx, http.MethodGet, nil)
}

func (c *HTTPCoordinator) Report(ctx context.Context, reportID string, reported uint64) (Window, error) {
	// Report ids are generated locally and must satisfy the coordinator's validation;
	// checking here turns a silent 400 loop into an immediate, legible failure.
	if len(reportID) < 8 || len(reportID) > 80 {
		return Window{}, fmt.Errorf("report id must be 8..80 characters, got %d", len(reportID))
	}
	body, err := json.Marshal(struct {
		ReportID string `json:"reportId"`
		Bytes    uint64 `json:"bytes"`
	}{reportID, reported})
	if err != nil {
		return Window{}, err
	}
	return c.call(ctx, http.MethodPost, body)
}

func (c *HTTPCoordinator) call(ctx context.Context, method string, body []byte) (Window, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method,
		fmt.Sprintf("%s/api/hosts/%s/bandwidth", c.base, c.hostID), reader)
	if err != nil {
		return Window{}, err
	}
	request.Header.Set("authorization", "Bearer "+c.token)
	if body != nil {
		request.Header.Set("content-type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return Window{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		// The upstream body is NOT included: it can echo request content, and this error
		// reaches logs. The status is enough to tell a contract bug (4xx) from an outage.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return Window{}, fmt.Errorf("coordinator bandwidth endpoint returned %d", response.StatusCode)
	}
	var wire wireWindow
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&wire); err != nil {
		return Window{}, fmt.Errorf("coordinator bandwidth response was unreadable: %w", err)
	}
	if wire.LimitBytes == 0 {
		// A zero limit would read as "no budget configured" and, through
		// effectiveLimitLocked, as a cold start. Refusing it keeps the previous window in
		// force instead of letting a malformed answer become an allowance.
		return Window{}, errors.New("coordinator returned no monthly limit")
	}
	window := Window{
		LimitBytes: wire.LimitBytes, UsedBytes: wire.UsedBytes,
		RemainingBytes: wire.RemainingBytes, SustainedBytesPerSecond: wire.SustainedBytesPerSecond,
		OfflineFloorBytesPerSecond: wire.OfflineFloorBytesPerSecond,
		WindowDays:                 wire.Window.Days, Exhausted: wire.Exhausted,
	}
	if window.WindowDays == 0 {
		window.WindowDays = WindowDays
	}
	return window, nil
}
