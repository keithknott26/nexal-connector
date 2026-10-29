package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexal/connector/internal/mesh"
)

func TestReportTunnelStatusRejectsInvalidHostID(t *testing.T) {
	api, _ := New("https://example.invalid", "host-secret", true)
	if err := api.ReportTunnelStatus(context.Background(), "../bad", mesh.Status{}); err == nil || err.Error() != "invalid host id" {
		t.Fatalf("got %v, want invalid host id", err)
	}
}

func TestReportTunnelStatusPostsAndHonorsAcceptance(t *testing.T) {
	var method, path string
	body := `{"ok":true,"extra":"ignored"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer server.Close()
	api, _ := New(server.URL, "host-secret", true)
	if err := api.ReportTunnelStatus(context.Background(), "host_1", mesh.Status{}); err != nil {
		t.Fatalf("accepted report failed: %v", err)
	}
	if method != "POST" || path != "/api/v2/devices/host_1/tunnel-status" {
		t.Fatalf("request = %s %s", method, path)
	}
	body = `{"ok":false}`
	err := api.ReportTunnelStatus(context.Background(), "host_1", mesh.Status{})
	if err == nil || !strings.Contains(err.Error(), "did not accept") {
		t.Fatalf("got %v, want not-accepted error", err)
	}
}
