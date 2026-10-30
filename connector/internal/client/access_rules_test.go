package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func accessRuleTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(server.URL, "host-credential-value", true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func validTestRule() AccessRule {
	return AccessRule{ID: "rule-one", NetworkID: "network-one", SourceComputerID: "device-one", TargetComputerID: "device-two", Protocol: "tcp", PortStart: 443, PortEnd: 443, Action: "allow", Enabled: true, Revision: 1}
}

func TestAccessRulesRejectsBadHostAndMalformedSets(t *testing.T) {
	calls := 0
	var reply map[string]any
	c := accessRuleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(reply)
	})
	if _, err := c.AccessRules(context.Background(), "bad/host"); err == nil || calls != 0 {
		t.Fatalf("invalid host id must fail before any request: err=%v calls=%d", err, calls)
	}
	bad := validTestRule()
	bad.Action = "permit"
	for name, r := range map[string]map[string]any{
		"allow default":  {"defaultAction": "allow", "deviceId": "device-one", "networkId": "network-one", "rules": []any{}},
		"missing device": {"defaultAction": "deny", "deviceId": "", "networkId": "network-one", "rules": []any{}},
		"invalid rule":   {"defaultAction": "deny", "deviceId": "device-one", "networkId": "network-one", "rules": []any{bad}},
	} {
		reply = r
		if _, err := c.AccessRules(context.Background(), "host-one"); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestAcknowledgeAccessRuleRejectsInvalidInputAndMismatchedReply(t *testing.T) {
	calls := 0
	var reply map[string]any
	c := accessRuleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(reply)
	})
	rule := validTestRule()
	disabled := rule
	disabled.Enabled = false
	for name, fn := range map[string]func() error{
		"bad host": func() error { return c.AcknowledgeAccessRule(context.Background(), "bad/host", rule, "applied", "") },
		"invalid rule": func() error {
			return c.AcknowledgeAccessRule(context.Background(), "host-one", disabled, "applied", "")
		},
		"bad state": func() error { return c.AcknowledgeAccessRule(context.Background(), "host-one", rule, "pending", "") },
		"long detail": func() error {
			return c.AcknowledgeAccessRule(context.Background(), "host-one", rule, "failed", strings.Repeat("x", 81))
		},
	} {
		if err := fn(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input reached the server %d times", calls)
	}
	for name, field := range map[string]struct {
		key   string
		value any
	}{
		"not acknowledged": {"acknowledged", false},
		"other rule":       {"ruleId", "rule-two"},
		"other revision":   {"revision", 2},
		"other state":      {"state", "failed"},
	} {
		reply = map[string]any{"acknowledged": true, "ruleId": "rule-one", "revision": 1, "state": "applied"}
		reply[field.key] = field.value
		if err := c.AcknowledgeAccessRule(context.Background(), "host-one", rule, "applied", ""); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
