package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// coordinatorBody mirrors the object apps/coordinator/src/peers.ts returns for
// GET /api/peers, including every field the connector does not consume.
// Timestamps are ISO-8601 TEXT, matching migration 0001 and 0011 column types,
// identityEvidence is the coordinator's IDENTITY_EVIDENCE constant verbatim, and
// peerLimit is its MAX_PEERS. All three were wrong in the fixture this replaces.
//
// This test exists because both repos' suites passed while WAN discovery was
// broken end to end: each side tested against its own fixture, and the client
// decodes with DisallowUnknownFields, so the server's extra fields were a hard
// failure that neither suite could observe. See HARDENING-PLAN §43.
const coordinatorBody = `{
  "peers": [{
    "hostId": "h_0123456789abcdef",
    "name": "studio",
    "fingerprint": "abababababababababababababababababababababababababababababababab",
    "lastSeenAt": "2026-09-20T23:00:00.000Z",
    "addresses": [
      {"kind":"lan","address":"192.168.1.10","port":7443,"observedAt":"2026-09-20T23:00:00.000Z"},
      {"kind":"wan","address":"203.0.113.7","port":7443,"observedAt":"2026-09-20T23:00:00.000Z"}
    ],
    "capabilities": {"thunderboltGeneration":5,"rdmaEnabled":true,"chip":"M4 Max","osVersion":"26.2"},
    "capabilitiesReportedAt": "2026-09-20T23:00:00.000Z"
  }],
  "peerLimit": 200,
  "truncated": false,
  "freshnessSeconds": 900,
  "authorization": "candidate-list-not-authorization",
  "capabilityEvidence": "self-reported-not-attested",
  "identityEvidence": "self-reported-not-proof-of-key-possession"
}`

func directoryFrom(t *testing.T, body string) (PeerDirectory, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	// dev=true: httptest serves plain HTTP, which ValidateURL rejects in production mode.
	c, err := New(srv.URL, "t_0123456789abcdef", true)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c.Peers(context.Background())
}

func TestPeersAcceptsRealCoordinatorResponse(t *testing.T) {
	got, err := directoryFrom(t, coordinatorBody)
	if err != nil {
		t.Fatalf("real coordinator response rejected: %v", err)
	}
	if len(got.Peers) != 1 {
		t.Fatalf("peers = %d, want 1", len(got.Peers))
	}
	p := got.Peers[0]
	if p.LastSeenAt == "" || p.CapabilitiesReportedAt == "" {
		t.Error("coordinator timestamps were dropped")
	}
	if len(p.Addresses) != 2 || p.Addresses[0].ObservedAt == "" {
		t.Error("address observedAt was dropped")
	}
	if !p.SelfReportedCapabilities.RDMAEnabled || p.SelfReportedCapabilities.ThunderboltGeneration != 5 {
		t.Error("capabilities were dropped")
	}
	if got.FreshnessSeconds != 900 || got.PeerLimit != 200 || got.Truncated {
		t.Errorf("pagination/freshness dropped: %+v", got)
	}
}

// A null capabilities object is what the coordinator sends for a peer that has
// never advertised. It must decode to the zero value, not fail the directory.
func TestPeersAcceptsNullCapabilities(t *testing.T) {
	body := `{"peers":[{"hostId":"h_0123456789abcdef","name":"n","fingerprint":"abababababababababababababababababababababababababababababababab",
	  "lastSeenAt":"2026-09-20T23:00:00.000Z","addresses":[],
	  "capabilities":null,"capabilitiesReportedAt":null}],
	  "peerLimit":200,"truncated":false,"freshnessSeconds":900,
	  "authorization":"candidate-list-not-authorization",
	  "capabilityEvidence":"self-reported-not-attested",
	  "identityEvidence":"self-reported-not-proof-of-key-possession"}`
	got, err := directoryFrom(t, body)
	if err != nil {
		t.Fatalf("null capabilities rejected: %v", err)
	}
	if got.Peers[0].SelfReportedCapabilities.RDMAEnabled {
		t.Error("null capabilities must not report RDMA")
	}
}

// The honesty fields are load-bearing. A response missing identityEvidence is
// not from something that shares this contract and must be refused, not read.
func TestPeersRefusesMissingIdentityEvidence(t *testing.T) {
	body := strings.Replace(coordinatorBody,
		`"identityEvidence": "self-reported-not-proof-of-key-possession"`,
		`"identityEvidence": ""`, 1)
	if _, err := directoryFrom(t, body); err == nil {
		t.Fatal("directory without identityEvidence was accepted")
	}
}

func TestPeersRefusesWeakenedAuthorizationString(t *testing.T) {
	body := strings.Replace(coordinatorBody,
		"candidate-list-not-authorization", "authorized-peers", 1)
	if _, err := directoryFrom(t, body); err == nil {
		t.Fatal("directory claiming to be authorization was accepted")
	}
}

// The client rejects an oversized directory outright rather than truncating it,
// so maxDirectoryPeers below the coordinator's MAX_PEERS would disable discovery
// for large tenants only — invisible in small-fleet testing. 200 is the server's
// page size in apps/coordinator/src/peers.ts.
func TestDirectoryCapAcceptsAFullServerPage(t *testing.T) {
	const serverMaxPeers = 200
	if maxDirectoryPeers < serverMaxPeers {
		t.Fatalf("maxDirectoryPeers=%d rejects a full server page of %d",
			maxDirectoryPeers, serverMaxPeers)
	}
	var b strings.Builder
	b.WriteString(`{"peers":[`)
	for i := 0; i < serverMaxPeers; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"hostId":"h_%016x","name":"n","fingerprint":"abababababababababababababababababababababababababababababababab",`+
			`"lastSeenAt":"2026-09-20T23:00:00.000Z","addresses":[],`+
			`"capabilities":null,"capabilitiesReportedAt":null}`, i)
	}
	b.WriteString(`],"peerLimit":200,"truncated":false,"freshnessSeconds":900,` +
		`"authorization":"candidate-list-not-authorization",` +
		`"capabilityEvidence":"self-reported-not-attested",` +
		`"identityEvidence":"self-reported-not-proof-of-key-possession"}`)
	got, err := directoryFrom(t, b.String())
	if err != nil {
		t.Fatalf("full server page rejected: %v", err)
	}
	if len(got.Peers) != serverMaxPeers {
		t.Fatalf("peers = %d, want %d", len(got.Peers), serverMaxPeers)
	}
}

// A directory above the server's own page size is not a bigger tenant, it is a
// response that does not share this contract. It must still be refused.
func TestDirectoryAboveCapIsRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"peers":[`)
	for i := 0; i < maxDirectoryPeers+1; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"hostId":"h_%016x","name":"n","fingerprint":"abababababababababababababababababababababababababababababababab",`+
			`"lastSeenAt":"2026-09-20T23:00:00.000Z","addresses":[],`+
			`"capabilities":null,"capabilitiesReportedAt":null}`, i)
	}
	b.WriteString(`],"peerLimit":200,"truncated":true,"freshnessSeconds":900,` +
		`"authorization":"candidate-list-not-authorization",` +
		`"capabilityEvidence":"self-reported-not-attested",` +
		`"identityEvidence":"self-reported-not-proof-of-key-possession"}`)
	if _, err := directoryFrom(t, b.String()); err == nil {
		t.Fatal("directory above the contract cap was accepted")
	}
}

// Both exact identityEvidence spellings are accepted on the directory path too,
// for the one-directional version skew described in HARDENING-PLAN §43.2:
// the coordinator deploys instantly, installed connectors do not.
func TestPeersAcceptsEitherExactIdentityEvidence(t *testing.T) {
	body := strings.Replace(coordinatorBody, IdentityEvidenceSelfReported,
		IdentityEvidenceEnrollmentBound, 1)
	if _, err := directoryFrom(t, body); err != nil {
		t.Fatalf("enrollment-bound identityEvidence rejected: %v", err)
	}
	other := strings.Replace(coordinatorBody, IdentityEvidenceSelfReported, "attested", 1)
	if _, err := directoryFrom(t, other); err == nil {
		t.Fatal("a third identityEvidence spelling was accepted")
	}
}
