package budget

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OFFLINE: httptest binds loopback only. Nothing here resolves a name or leaves the
// machine, and no test in this package contacts Cloudflare or the real coordinator.

func devCoordinator(t *testing.T, handler http.HandlerFunc) (*HTTPCoordinator, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	// dev=true is what permits plaintext loopback; config.ValidateURL refuses it otherwise,
	// which is the property the next test asserts.
	coordinator, err := NewHTTPCoordinator(server.URL, "host-1", "host_token_value", true)
	if err != nil {
		t.Fatalf("NewHTTPCoordinator: %v", err)
	}
	// The default transport pins TLS 1.3, which a plaintext loopback test server cannot
	// speak; swapping the client here keeps the TLS floor in production code and still
	// exercises the request construction and parsing that the budget depends on.
	coordinator.client = server.Client()
	return coordinator, server
}

const windowPayload = `{"limitBytes":268435456000,"usedBytes":1073741824,"remainingBytes":267361714176,
  "sustainedBytesPerSecond":103563,"offlineFloorBytesPerSecond":103563,"exhausted":false,
  "window":{"days":30,"kind":"rolling-utc-day-buckets"},"history":{"sampled":true},
  "enforcement":{"coordinatorBlocksTransfers":false},"aFieldThisBuildHasNeverSeen":true}`

func TestHTTPCoordinatorReportsAndParses(t *testing.T) {
	var method, path, auth string
	var body struct {
		ReportID string `json:"reportId"`
		Bytes    uint64 `json:"bytes"`
	}
	coordinator, _ := devCoordinator(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(windowPayload))
	})
	window, err := coordinator.Report(context.Background(), "report-identifier-1", 4096)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if method != http.MethodPost || path != "/api/hosts/host-1/bandwidth" {
		t.Fatalf("wrong request: %s %s", method, path)
	}
	if auth != "Bearer host_token_value" {
		t.Fatalf("the host token must authorize the report, got %q", auth)
	}
	if body.ReportID != "report-identifier-1" || body.Bytes != 4096 {
		t.Fatalf("report body %+v", body)
	}
	if window.LimitBytes != 268435456000 || window.UsedBytes != 1073741824 {
		t.Fatalf("parsed window %+v", window)
	}
	if window.OfflineFloorBytesPerSecond != 103563 || window.WindowDays != 30 {
		t.Fatalf("the floor and window length must survive parsing: %+v", window)
	}
	// The unknown field is the point: a newer coordinator payload must not break an older
	// connector build, because a connector that cannot parse stops enforcing the real
	// budget and silently parks on the conservative floor.
}

func TestHTTPCoordinatorWindowDoesNotWrite(t *testing.T) {
	var method string
	coordinator, _ := devCoordinator(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write([]byte(windowPayload))
	})
	if _, err := coordinator.Window(context.Background()); err != nil {
		t.Fatalf("Window: %v", err)
	}
	if method != http.MethodGet {
		t.Fatalf("reading the window must be a GET, got %s", method)
	}
}

func TestHTTPCoordinatorRefusesUnusableAnswers(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload string
	}{
		// A zero limit would flow into effectiveLimitLocked as "cold start", so a
		// malformed answer must be an error rather than a new allowance.
		{"zero limit", 200, `{"limitBytes":0,"usedBytes":0}`},
		{"not json", 200, `<html>a captive portal</html>`},
		{"server error", 500, `{"error":"boom"}`},
		{"unauthorized", 401, `{"error":"invalid_token"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			coordinator, _ := devCoordinator(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.payload))
			})
			if _, err := coordinator.Report(context.Background(), "report-identifier-2", 1); err == nil {
				t.Fatal("expected an error rather than an allowance")
			}
		})
	}
}

func TestHTTPCoordinatorErrorsNeverEchoUpstreamBodies(t *testing.T) {
	coordinator, _ := devCoordinator(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("token host_token_value rejected for host-1"))
	})
	_, err := coordinator.Report(context.Background(), "report-identifier-3", 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "host_token_value") {
		t.Fatalf("an upstream body reached a logged error: %v", err)
	}
}

func TestHTTPCoordinatorValidatesItsInputs(t *testing.T) {
	cases := []struct {
		name, base, host, token string
		dev                     bool
	}{
		{"plaintext off loopback", "http://coordinator.example.com", "host-1", "t", true},
		{"url with a path", "https://coordinator.example.com/api", "host-1", "t", false},
		{"credentials in the url", "https://user:pw@coordinator.example.com", "host-1", "t", false},
		{"no host id", "https://coordinator.example.com", "", "t", false},
		{"no token", "https://coordinator.example.com", "host-1", "", false},
		// A host id that needs escaping could redirect the report at another path.
		{"host id escaping the path segment", "https://coordinator.example.com", "host/../../admin", "t", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewHTTPCoordinator(testCase.base, testCase.host, testCase.token, testCase.dev); err == nil {
				t.Fatal("expected the configuration to be refused")
			}
		})
	}
	if _, err := NewHTTPCoordinator("https://coordinator.nexal.systems", "host-1", "token", false); err != nil {
		t.Fatalf("a valid origin must be accepted: %v", err)
	}
}

func TestHTTPCoordinatorRejectsOutOfBoundReportIdentifiers(t *testing.T) {
	// Caught locally so a drift from the coordinator's 8..80 validation is an immediate,
	// legible error instead of a 400 loop that looks like an outage.
	coordinator, _ := devCoordinator(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(windowPayload))
	})
	for _, id := range []string{"", "short", strings.Repeat("x", 81)} {
		if _, err := coordinator.Report(context.Background(), id, 1); err == nil {
			t.Fatalf("report id %q should have been refused locally", id)
		}
	}
}

func TestHTTPCoordinatorStopsReadingOversizedResponses(t *testing.T) {
	// A hostile or misrouted endpoint must not be able to exhaust memory on a donor's
	// machine. The read is bounded, so a huge body fails to parse rather than being buffered.
	coordinator, _ := devCoordinator(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"limitBytes":1,"pad":"` + strings.Repeat("a", maxResponseBytes*2) + `"}`))
	})
	if _, err := coordinator.Report(context.Background(), "report-identifier-4", 1); err == nil {
		t.Fatal("expected a bounded read to fail on an oversized body")
	}
}

func TestBudgetUsesTheHTTPCoordinatorEndToEnd(t *testing.T) {
	// The seam joined up, still entirely on loopback: a Budget whose Coordinator is the
	// real HTTP implementation enforces the window the server returned.
	coordinator, _ := devCoordinator(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(windowPayload))
	})
	budget, err := New(coordinator, newClock())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := budget.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	allowance := budget.Allowance()
	if allowance.LimitBytes != 268435456000 || allowance.RemainingBytes != 268435456000-1073741824 {
		t.Fatalf("allowance %+v", allowance)
	}
	if allowance.Capped {
		t.Fatal("a budget with 99% remaining should impose no rate cut")
	}
}
