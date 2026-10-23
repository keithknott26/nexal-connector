package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nexal/connector/internal/timemachine"
)

func TestClientConfigFromRoleHandling(t *testing.T) {
	legacy := map[string]json.RawMessage{"enabled": json.RawMessage(`true`)}
	if cfg, err := clientConfigFrom(legacy); err != nil || cfg.Role != "" {
		t.Fatalf("document without role must stay empty: %+v %v", cfg, err)
	}
	server := map[string]json.RawMessage{"role": json.RawMessage(`"server"`)}
	if cfg, err := clientConfigFrom(server); err != nil || cfg.Role != "" {
		t.Fatalf("server role must stay empty: %+v %v", cfg, err)
	}
	badRole := map[string]json.RawMessage{"role": json.RawMessage(`5`)}
	if cfg, err := clientConfigFrom(badRole); err != nil || cfg.Role != "" {
		t.Fatalf("non-string role must be treated as legacy: %+v %v", cfg, err)
	}
	ok := map[string]json.RawMessage{"role": json.RawMessage(`"client"`), "enabled": json.RawMessage(`false`), "computerId": json.RawMessage(`"c1"`)}
	if cfg, err := clientConfigFrom(ok); err != nil || cfg.Role != timemachine.RoleClient || cfg.ComputerID != "c1" {
		t.Fatalf("client role: %+v %v", cfg, err)
	}
	unknown := map[string]json.RawMessage{"role": json.RawMessage(`"client"`), "surprise": json.RawMessage(`1`)}
	if _, err := clientConfigFrom(unknown); err == nil {
		t.Fatal("unknown fields in a client document must be rejected")
	}
}

func TestTimeMachineEnrollAndUsageContract(t *testing.T) {
	var usage timemachine.Usage
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v2/devices/host1/time-machine/enroll":
			_ = json.NewEncoder(w).Encode(map[string]any{"role": "client", "enabled": false, "computerId": "c1", "networkId": "n1"})
		case "GET /api/v2/devices/host1/time-machine/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"role": "client", "enabled": false, "computerId": "c1"})
		case "POST /api/v2/devices/host1/time-machine/usage":
			_ = json.NewDecoder(r.Body).Decode(&usage)
			_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": usage.UsedBytes != 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c, err := New(s.URL, "host-token-123456", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if cfg, err := c.EnrollTimeMachine(ctx, "host1"); err != nil || cfg.NetworkID != "n1" {
		t.Fatalf("enroll: %+v %v", cfg, err)
	}
	if cfg, err := c.TimeMachineClientConfig(ctx, "host1"); err != nil || cfg.Role != timemachine.RoleClient {
		t.Fatalf("client config: %+v %v", cfg, err)
	}
	sample := timemachine.Usage{SampleID: "sample-0001", UsedBytes: 42, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := c.ReportTimeMachineUsage(ctx, "host1", sample); err != nil || usage.UsedBytes != 42 {
		t.Fatalf("usage: %v %+v", err, usage)
	}
	sample.UsedBytes = 0
	if err := c.ReportTimeMachineUsage(ctx, "host1", sample); err == nil {
		t.Fatal("unaccepted usage must be an error")
	}
	if err := c.ReportTimeMachineUsage(ctx, "host1", timemachine.Usage{SampleID: "x"}); err == nil {
		t.Fatal("invalid sample must be rejected before any request")
	}
}

func TestTimeMachineRejectsInvalidHostID(t *testing.T) {
	c, err := New("http://127.0.0.1:1", "host-token-123456", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.EnrollTimeMachine(ctx, "bad/id"); err == nil {
		t.Error("enroll accepted invalid host id")
	}
	if _, err := c.TimeMachineClientConfig(ctx, "bad/id"); err == nil {
		t.Error("client config accepted invalid host id")
	}
	if _, err := c.TimeMachineCredentials(ctx, "bad/id"); err == nil {
		t.Error("credentials accepted invalid host id")
	}
	if err := c.ReportTimeMachineUsage(ctx, "bad/id", timemachine.Usage{}); err == nil {
		t.Error("usage accepted invalid host id")
	}
}
