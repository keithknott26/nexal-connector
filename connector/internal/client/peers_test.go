package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func peerDirectoryServer(t *testing.T, body any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/peers" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer host-scoped-secret" {
			t.Error("peer directory read without the host token")
		}
		w.Header().Set("Content-Type", "application/json")
		ioJSON(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "host-scoped-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPeerDirectoryRead(t *testing.T) {
	c := peerDirectoryServer(t, map[string]any{
		"authorization":      AuthorizationCandidateList,
		"capabilityEvidence": CapabilityEvidenceSelfReported,
		"identityEvidence":   IdentityEvidenceSelfReported,
		"peers": []map[string]any{{
			"hostId":      "host-2",
			"name":        "Studio",
			"fingerprint": strings.Repeat("ab", 32),
			"addresses": []map[string]any{
				{"kind": "lan", "address": "192.168.4.7", "port": 8443},
				{"kind": "wan", "address": "93.184.216.34", "port": 8443},
			},
			"capabilities": map[string]any{"chip": "M4 Pro", "thunderboltGeneration": 5, "rdmaEnabled": true, "osVersion": "26.2"},
		}},
	})
	directory, err := c.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.Peers) != 1 || directory.Peers[0].HostID != "host-2" {
		t.Fatalf("directory %+v", directory)
	}
	if directory.Peers[0].SelfReportedCapabilities.Chip != "M4 Pro" {
		t.Fatalf("capabilities %+v", directory.Peers[0].SelfReportedCapabilities)
	}
	if len(directory.Peers[0].Addresses) != 2 || directory.Peers[0].Addresses[0].Kind != "lan" {
		t.Fatalf("addresses %+v", directory.Peers[0].Addresses)
	}
}

// TestPeerDirectoryRefusesResponsesThatDoNotShareTheContract keeps the honesty
// fields load-bearing. A response that does not declare itself a candidate list
// is from something that does not share this contract, and guessing at its
// meaning is how a candidate list quietly becomes an authorization.
func TestPeerDirectoryRefusesResponsesThatDoNotShareTheContract(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{"missing authorization field", map[string]any{
			"capabilityEvidence": CapabilityEvidenceSelfReported,
			"identityEvidence":   IdentityEvidenceSelfReported, "peers": []any{}}},
		{"authorization claims more than it should", map[string]any{
			"authorization": "authorized", "capabilityEvidence": CapabilityEvidenceSelfReported,
			"identityEvidence": IdentityEvidenceSelfReported, "peers": []any{}}},
		{"missing capability disclaimer", map[string]any{
			"authorization":    AuthorizationCandidateList,
			"identityEvidence": IdentityEvidenceSelfReported, "peers": []any{}}},
		{"unknown field", map[string]any{
			"authorization": AuthorizationCandidateList, "capabilityEvidence": CapabilityEvidenceSelfReported,
			"identityEvidence": IdentityEvidenceSelfReported,
			"peers":            []any{}, "trustAll": true}},
		{"invalid host id", map[string]any{
			"authorization": AuthorizationCandidateList, "capabilityEvidence": CapabilityEvidenceSelfReported,
			"identityEvidence": IdentityEvidenceSelfReported,
			"peers":            []map[string]any{{"hostId": "host 2/../", "name": "", "fingerprint": strings.Repeat("ab", 32), "addresses": []any{}, "capabilities": map[string]any{}}}}},
		{"unknown address kind", map[string]any{
			"authorization": AuthorizationCandidateList, "capabilityEvidence": CapabilityEvidenceSelfReported,
			"identityEvidence": IdentityEvidenceSelfReported,
			"peers": []map[string]any{{"hostId": "host-2", "name": "", "fingerprint": strings.Repeat("ab", 32),
				"addresses":    []map[string]any{{"kind": "relay", "address": "192.168.4.7", "port": 8443}},
				"capabilities": map[string]any{}}}}},
		{"more addresses than the server cap", map[string]any{
			"authorization": AuthorizationCandidateList, "capabilityEvidence": CapabilityEvidenceSelfReported,
			"identityEvidence": IdentityEvidenceSelfReported,
			"peers": []map[string]any{{"hostId": "host-2", "name": "", "fingerprint": strings.Repeat("ab", 32),
				"addresses":    tooManyAddresses(),
				"capabilities": map[string]any{}}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := peerDirectoryServer(t, tc.body)
			if _, err := c.Peers(context.Background()); err == nil {
				t.Fatal("accepted a peer directory that does not match the contract")
			}
		})
	}
}

func tooManyAddresses() []map[string]any {
	var out []map[string]any
	for i := range maxDirectoryAddressesPerPeer + 1 {
		out = append(out, map[string]any{"kind": "lan", "address": "192.168.4.7", "port": 8000 + i})
	}
	return out
}
