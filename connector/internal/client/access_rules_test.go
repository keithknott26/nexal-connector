package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessRulesAreDefaultDenyAndAcknowledgedExactly(t *testing.T) {
	ruleID := "123e4567-e89b-12d3-a456-426614174001"
	hostID := "host-one"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer host-credential-value" {
			t.Errorf("missing host bearer")
		}
		if r.URL.Path != "/api/v2/devices/"+hostID+"/access-rules" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"defaultAction": "deny", "deviceId": "device-one", "networkId": "network-one", "rules": []any{
				map[string]any{"id": ruleID, "networkId": "network-one", "sourceComputerId": "device-one", "targetComputerId": "device-two", "protocol": "tcp", "portStart": 443, "portEnd": 443, "action": "allow", "enabled": true, "revision": 1},
			}})
			return
		}
		var body accessRuleApply
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.RuleID != ruleID || body.Revision != 1 || body.State != "applied" {
			t.Fatalf("ack %#v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"acknowledged": true, "ruleId": ruleID, "revision": 1, "state": "applied"})
	}))
	defer server.Close()
	c, err := New(server.URL, "host-credential-value", true)
	if err != nil {
		t.Fatal(err)
	}
	set, err := c.AccessRules(context.Background(), hostID)
	if err != nil {
		t.Fatal(err)
	}
	if set.DefaultAction != "deny" || len(set.Rules) != 1 {
		t.Fatalf("rules %#v", set)
	}
	if err = c.AcknowledgeAccessRule(context.Background(), hostID, set.Rules[0], "applied", ""); err != nil {
		t.Fatal(err)
	}
}

func TestAccessRulesRejectUnsafeForwarding(t *testing.T) {
	forwarder := "different-device"
	listen, target := 8443, 443
	rule := AccessRule{ID: "rule-one", NetworkID: "network-one", SourceComputerID: "device-one", TargetComputerID: "device-two", Protocol: "tcp", PortStart: 443, PortEnd: 443, Action: "allow", Enabled: true, Revision: 1, ForwardingComputerID: &forwarder, ListenPort: &listen, ForwardTargetPort: &target}
	if validateRule(rule) == nil {
		t.Fatal("forwarding on a computer other than the source must fail")
	}
}
