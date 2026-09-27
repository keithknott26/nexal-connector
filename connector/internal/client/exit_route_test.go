package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSourceDevice = "11111111-1111-4111-8111-111111111111"
const testTargetDevice = "22222222-2222-4222-8222-222222222222"

func exitAck(enabled bool) ExitRouteAck {
	readiness := "not_configured"
	if enabled {
		readiness = "awaiting_client_verification"
	}
	return ExitRouteAck{SchemaVersion: 1, NetworkID: "network", SourceDeviceID: testSourceDevice, TargetDeviceID: testTargetDevice, RouteID: "nx-exit-" + strings.Repeat("a", 32), Enabled: enabled, Configured: enabled, SelectionRequired: enabled, Readiness: readiness, IPv6: "provider_managed"}
}
func TestExitRouteAuthorityAndStableDisableTarget(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/hosts/host_1/exit-route" || r.Header.Get("Authorization") != "Bearer host-secret" {
			t.Error("wrong host authority or endpoint")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		enabled, _ := body["enabled"].(bool)
		if enabled {
			if body["targetTunnelAddress"] != "100.70.1.2" || body["targetDeviceId"] != nil {
				t.Error("wrong enable body", body)
			}
		} else {
			if body["targetDeviceId"] != testTargetDevice || body["targetTunnelAddress"] != nil {
				t.Error("stable disable ID must omit address", body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(exitAck(enabled))
	}))
	defer server.Close()
	api, err := New(server.URL, "host-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ConfigureExitRoute(context.Background(), "host_1", "100.70.1.2", "", true); err != nil {
		t.Fatal(err)
	}
	// Even a stale/invalid supplied address is not transmitted during ID teardown.
	if _, err := api.ConfigureExitRoute(context.Background(), "host_1", "old-address", testTargetDevice, false); err != nil {
		t.Fatal(err)
	}
	if _, err := api.ConfigureExitRoute(context.Background(), "host_1", "100.70.1.2", testTargetDevice, true); err == nil {
		t.Fatal("enable accepted target device ID")
	}
	if _, err := api.ConfigureExitRoute(context.Background(), "host_1", "8.8.8.8", "", true); err == nil {
		t.Fatal("public address accepted")
	}
	if calls != 2 {
		t.Fatal("invalid input reached server", calls)
	}
}
func TestExitRouteRejectsWrongAcknowledgementAndPreservesProviderErrors(t *testing.T) {
	mode := "wrong-route"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if mode == "error" {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":{"code":"exit_route_unavailable","message":"secret backend detail"}}`))
			return
		}
		ack := exitAck(true)
		switch mode {
		case "wrong-route":
			ack.RouteID = "../../evil"
		case "auto-apply":
			ack.AutoApply = true
		case "not-configured":
			ack.Configured = false
		case "same-peer":
			ack.TargetDeviceID = ack.SourceDeviceID
		}
		json.NewEncoder(w).Encode(ack)
	}))
	defer server.Close()
	api, _ := New(server.URL, "host-secret", true)
	for _, value := range []string{"wrong-route", "auto-apply", "not-configured", "same-peer"} {
		mode = value
		if _, err := api.ConfigureExitRoute(context.Background(), "host_1", "100.70.1.2", "", true); err == nil {
			t.Fatal("invalid acknowledgement accepted", mode)
		}
	}
	mode = "error"
	_, err := api.ConfigureExitRoute(context.Background(), "host_1", "100.70.1.2", "", true)
	var rejected *StatusError
	if !errors.As(err, &rejected) || rejected.Status != 503 || rejected.Code != "exit_route_unavailable" || strings.Contains(err.Error(), "secret") {
		t.Fatal("lost safe provider error", err)
	}
}
