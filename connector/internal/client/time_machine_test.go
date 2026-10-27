package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nexal/connector/internal/timemachine"
)

func TestTimeMachineCoordinatorContract(t *testing.T) {
	var reported struct{ State, ErrorCode string }
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer host-token-123456" {
			t.Error("missing host auth")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v2/devices/host1/time-machine/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "serviceState": "provisioning", "computerId": "computer1", "networkId": "network1", "protocol": "smb", "port": 445, "bonjour": map[string]any{"serviceType": "_adisk._tcp", "shareName": "NexalBackup"}, "storage": map[string]any{"driver": "juicefs", "backend": "r2", "objectPrefix": "tenants/t1/time-machine/network1/", "credentialEndpoint": "/api/v2/devices/host1/time-machine/credentials", "credentialsIncluded": false}, "quotaBytes": uint64(100 << 30), "refreshAfterSeconds": 60})
		case "POST /api/v2/devices/host1/time-machine/status":
			if err := json.NewDecoder(r.Body).Decode(&reported); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c, err := New(s.URL, "host-token-123456", true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := c.TimeMachineConfig(context.Background(), "host1")
	if err != nil || cfg.Revision != 60 {
		t.Fatalf("%+v %v", cfg, err)
	}
	want := timemachine.Evaluate(cfg, timemachine.Observation{Platform: "linux", BackendMounted: true, BackendType: timemachine.BackendJuiceFS, FreeBytes: 200 << 30, SMBConfigured: true, SMBHealthy: true, Advertised: true, AdminAvailable: true})
	if err := c.ReportTimeMachineStatus(context.Background(), "host1", want); err != nil {
		t.Fatal(err)
	}
	if reported.State != "advertising" || reported.ErrorCode != "" {
		t.Fatalf("%+v", reported)
	}
}

func TestTimeMachineStatusRejectsBadHostAndUnacceptedReport(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": false})
	}))
	defer s.Close()
	c, err := New(s.URL, "host-token-123456", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.TimeMachineConfig(context.Background(), "bad/id"); err == nil {
		t.Fatal("TimeMachineConfig accepted an invalid host id")
	}
	if err := c.ReportTimeMachineStatus(context.Background(), "bad/id", timemachine.Status{}); err == nil {
		t.Fatal("ReportTimeMachineStatus accepted an invalid host id")
	}
	if calls != 0 {
		t.Fatalf("invalid host ids reached the coordinator %d times", calls)
	}
	if err := c.ReportTimeMachineStatus(context.Background(), "host1", timemachine.Status{}); err == nil {
		t.Fatal("an unaccepted status report returned nil")
	}
}

func TestStatusErrorTextAndCode(t *testing.T) {
	plain := &StatusError{Status: 409, Code: "no_wake_relay"}
	if plain.ErrorCode() != "no_wake_relay" {
		t.Fatalf("code = %q", plain.ErrorCode())
	}
	if got := plain.Error(); got != "coordinator rejected request (HTTP 409)" {
		t.Fatalf("plain = %q", got)
	}
	withMsg := &StatusError{Status: 400, Message: "bad input"}
	if got := withMsg.Error(); got != "coordinator rejected request (HTTP 400): bad input" {
		t.Fatalf("with message = %q", got)
	}
	if withMsg.ErrorCode() != "" {
		t.Fatalf("code = %q, want empty", withMsg.ErrorCode())
	}
}
