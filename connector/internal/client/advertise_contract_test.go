package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The fingerprint shape the coordinator enforces: 64 lowercase hex characters.
const testFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// advertiseBody is the LITERAL response of advertisePeer in
// apps/coordinator/src/peers.ts — every key of its return statement, with the
// types the Worker actually sends: ok is a boolean, lanAddresses an integer,
// observedWanAddress a string or null, and the three honesty fields the exact
// module constants (PEER_AUTHORIZATION, CAPABILITY_EVIDENCE, IDENTITY_EVIDENCE).
//
// Copied from the server, not authored to match AdvertiseAck. A fixture written
// on the same side as the struct under test cannot detect contract drift, which
// is precisely how the §43 bug survived two green test suites.
const advertiseBody = `{
  "ok": true,
  "fingerprint": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "lanAddresses": 2,
  "observedWanAddress": "203.0.113.7",
  "identityEvidence": "self-reported-not-proof-of-key-possession",
  "capabilityEvidence": "self-reported-not-attested",
  "authorization": "candidate-list-not-authorization"
}`

// emptyAdvertiseBody is the same response for a host that published its
// fingerprint and no address, which is what a machine with only an IPv6
// unique-local address or only a public address sends.
var emptyAdvertiseBody = strings.Replace(advertiseBody, `"lanAddresses": 2`, `"lanAddresses": 0`, 1)

// advertiseAgainst serves body for POST /api/peers/advertise and records the
// request body the client sent, so both directions of the contract are checked.
func advertiseAgainst(t *testing.T, body string, addresses []LANAddress, capabilities *PeerCapabilities) (AdvertiseAck, map[string]any, error) {
	t.Helper()
	var sent map[string]any
	var path, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	// dev=true: httptest serves plain HTTP, which ValidateURL refuses in production.
	c, err := New(srv.URL, "t_0123456789abcdef", true)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ack, err := c.Advertise(context.Background(), testFingerprint, addresses, capabilities)
	if err == nil && (path != "/api/peers/advertise" || method != "POST") {
		t.Fatalf("advertise used %s %s", method, path)
	}
	return ack, sent, err
}

func TestAdvertiseAcceptsRealCoordinatorResponse(t *testing.T) {
	ack, sent, err := advertiseAgainst(t, advertiseBody, []LANAddress{
		{Kind: "lan", Address: "192.168.1.10", Port: 7443},
		{Kind: "lan", Address: "10.0.0.4", Port: 7443},
	}, &PeerCapabilities{ThunderboltGeneration: 5, RDMAEnabled: true, OSVersion: "26.2", Chip: "M4 Max"})
	if err != nil {
		t.Fatalf("real coordinator advertise response rejected: %v", err)
	}
	if !ack.OK || ack.Fingerprint != testFingerprint {
		t.Errorf("ack fields dropped: %+v", ack)
	}
	if ack.LANAddresses != 2 {
		t.Errorf("lanAddresses = %d, want 2", ack.LANAddresses)
	}
	if ack.ObservedWANAddress != "203.0.113.7" {
		t.Errorf("observedWanAddress = %q", ack.ObservedWANAddress)
	}
	if ack.Authorization != AuthorizationCandidateList ||
		ack.CapabilityEvidence != CapabilityEvidenceSelfReported ||
		ack.IdentityEvidence != IdentityEvidenceSelfReported {
		t.Errorf("honesty fields dropped: %+v", ack)
	}
	// The request the server would have validated: exactly the three keys
	// advertisePeer allows, addresses as an array of {kind,address,port}.
	if len(sent) != 3 || sent["fingerprint"] != testFingerprint {
		t.Fatalf("request body = %v", sent)
	}
	list, ok := sent["addresses"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("addresses = %v", sent["addresses"])
	}
	first, _ := list[0].(map[string]any)
	if len(first) != 3 || first["kind"] != "lan" || first["address"] != "192.168.1.10" || first["port"] != float64(7443) {
		t.Fatalf("addresses[0] = %v", first)
	}
	caps, ok := sent["capabilities"].(map[string]any)
	if !ok || caps["rdmaEnabled"] != true || caps["thunderboltGeneration"] != float64(5) {
		t.Fatalf("capabilities = %v", sent["capabilities"])
	}
}

// Null is what the coordinator sends when the request did not arrive through a
// Cloudflare edge, which is every local development run. It means "no WAN
// candidate was recorded", and it must decode rather than fail the call.
func TestAdvertiseAcceptsNullObservedWANAddress(t *testing.T) {
	body := strings.Replace(emptyAdvertiseBody, `"observedWanAddress": "203.0.113.7"`,
		`"observedWanAddress": null`, 1)
	ack, _, err := advertiseAgainst(t, body, nil, nil)
	if err != nil {
		t.Fatalf("null observedWanAddress rejected: %v", err)
	}
	if ack.ObservedWANAddress != "" {
		t.Errorf("observedWanAddress = %q, want empty", ack.ObservedWANAddress)
	}
}

// An empty address set is a legitimate advertise: a host with no publishable
// address still publishes its fingerprint. It must serialize as [] and not null,
// because the server tests Array.isArray and returns 400 for null.
func TestAdvertiseSendsEmptyArrayNotNull(t *testing.T) {
	_, sent, err := advertiseAgainst(t, emptyAdvertiseBody, nil, nil)
	if err != nil {
		t.Fatalf("empty advertise rejected: %v", err)
	}
	raw, ok := sent["addresses"].([]any)
	if !ok || len(raw) != 0 {
		t.Fatalf("addresses = %#v, want []", sent["addresses"])
	}
	if _, present := sent["capabilities"]; present {
		t.Error("omitted capabilities must not be sent as null; the server clears on absence")
	}
}

func TestAdvertiseRefusesWeakenedHonestyStrings(t *testing.T) {
	for _, weaken := range []struct{ name, from, to string }{
		{"authorization", "candidate-list-not-authorization", "authorized-peers"},
		{"capabilityEvidence", "self-reported-not-attested", "attested"},
		{"identityEvidence", "self-reported-not-proof-of-key-possession", "hardware-attested"},
		{"identityEvidence-empty", "self-reported-not-proof-of-key-possession", ""},
	} {
		t.Run(weaken.name, func(t *testing.T) {
			body := strings.Replace(emptyAdvertiseBody, weaken.from, weaken.to, 1)
			if _, _, err := advertiseAgainst(t, body, nil, nil); err == nil {
				t.Fatalf("advertise accepted a response with %s weakened", weaken.name)
			}
		})
	}
}

// The other spelling of identityEvidence stays accepted, exactly, so a rename on
// either side of an independently-deployed boundary does not black-hole
// discovery for an already-installed fleet (§43.2).
func TestAdvertiseAcceptsEitherExactIdentityEvidence(t *testing.T) {
	body := strings.Replace(emptyAdvertiseBody, IdentityEvidenceSelfReported,
		IdentityEvidenceSelfReported, 1)
	if _, _, err := advertiseAgainst(t, body, nil, nil); err != nil {
		t.Fatalf("enrollment-bound identityEvidence rejected: %v", err)
	}
}

// ok:false, or an echoed fingerprint that is not the one we published, means the
// coordinator did not write this host's row. Neither is treated as success.
func TestAdvertiseRefusesUnacknowledgedWrite(t *testing.T) {
	for name, body := range map[string]string{
		"not-ok":          strings.Replace(emptyAdvertiseBody, `"ok": true`, `"ok": false`, 1),
		"other-identity":  strings.Replace(emptyAdvertiseBody, testFingerprint, strings.Repeat("a", 64), 1),
		"count-above-set": strings.Replace(advertiseBody, `"lanAddresses": 2`, `"lanAddresses": 9`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := advertiseAgainst(t, body, nil, nil); err == nil {
				t.Fatalf("advertise accepted %s", name)
			}
		})
	}
}

// Requests the coordinator would refuse are refused before they are sent: a
// rejected advertise leaves the previous address set in place, so a caller that
// learned about the problem from a 400 would silently keep stale addresses.
func TestAdvertiseRefusesRequestsTheServerWouldReject(t *testing.T) {
	over := make([]LANAddress, 0, MaxAdvertisedLANAddresses+1)
	for i := 0; i <= MaxAdvertisedLANAddresses; i++ {
		over = append(over, LANAddress{Kind: "lan", Address: "10.0.0." + string(rune('1'+i)), Port: 7443})
	}
	cases := map[string][]LANAddress{
		"wan kind":       {{Kind: "wan", Address: "10.0.0.4", Port: 7443}},
		"empty kind":     {{Address: "10.0.0.4", Port: 7443}},
		"zero port":      {{Kind: "lan", Address: "10.0.0.4"}},
		"public address": {{Kind: "lan", Address: "203.0.113.7", Port: 7443}},
		"loopback":       {{Kind: "lan", Address: "127.0.0.1", Port: 7443}},
		"ipv6 ula":       {{Kind: "lan", Address: "fd00::1", Port: 7443}},
		"zoned":          {{Kind: "lan", Address: "fe80::1%en0", Port: 7443}},
		"uppercase ipv6": {{Kind: "lan", Address: "FE80::1", Port: 7443}},
		"leading zeros":  {{Kind: "lan", Address: "010.0.0.1", Port: 7443}},
		"not an address": {{Kind: "lan", Address: "studio.local", Port: 7443}},
		"over cap":       over,
	}
	for name, addresses := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := advertiseAgainst(t, advertiseBody, addresses, nil); err == nil {
				t.Fatalf("advertise sent a request the coordinator refuses: %s", name)
			}
		})
	}
}

// fe80::/10 and 100.64/10 are two of the three families the server accepts, and
// duplicates are collapsed the way the server collapses them, so the cap counts
// candidates rather than the same address seen on two interfaces.
func TestAdvertiseAcceptsEveryServerLANFamilyAndCollapsesDuplicates(t *testing.T) {
	body := strings.Replace(advertiseBody, `"lanAddresses": 2`, `"lanAddresses": 4`, 1)
	_, sent, err := advertiseAgainst(t, body, []LANAddress{
		{Kind: "lan", Address: "172.16.5.9", Port: 7443},
		{Kind: "lan", Address: "100.64.1.2", Port: 7443},
		{Kind: "lan", Address: "169.254.10.11", Port: 7443},
		{Kind: "lan", Address: "fe80::1", Port: 7443},
		{Kind: "lan", Address: "172.16.5.9", Port: 7443},
	}, nil)
	if err != nil {
		t.Fatalf("server-accepted families rejected: %v", err)
	}
	if list, _ := sent["addresses"].([]any); len(list) != 4 {
		t.Fatalf("addresses = %v, want 4 after collapsing the duplicate", sent["addresses"])
	}
}

// coordinatorSource returns apps/coordinator/src/peers.ts when both repositories
// are checked out side by side. The drift test below skips when it is absent
// rather than failing, because the connector must build from its own repo alone.
func coordinatorSource(t *testing.T) string {
	t.Helper()
	const path = "../../../../nexal-platform/apps/coordinator/src/peers.ts"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("coordinator source not checked out alongside this repo: %v", err)
	}
	return string(b)
}

// The test §43 says every cross-repo constant needs: one that fails when the two
// sides disagree, reading the server's source rather than a copy of it.
func TestHonestyStringsAndCapsMatchCoordinatorSource(t *testing.T) {
	src := coordinatorSource(t)
	str := func(name string) string {
		m := regexp.MustCompile(`export const ` + name + ` = "([^"]*)";`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in peers.ts", name)
		}
		return m[1]
	}
	num := func(name string) string {
		m := regexp.MustCompile(`export const ` + name + ` = (\d+);`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in peers.ts", name)
		}
		return m[1]
	}
	if got := str("PEER_AUTHORIZATION"); got != AuthorizationCandidateList {
		t.Errorf("PEER_AUTHORIZATION = %q, client expects %q", got, AuthorizationCandidateList)
	}
	if got := str("CAPABILITY_EVIDENCE"); got != CapabilityEvidenceSelfReported {
		t.Errorf("CAPABILITY_EVIDENCE = %q, client expects %q", got, CapabilityEvidenceSelfReported)
	}
	if got := str("IDENTITY_EVIDENCE"); !acceptedIdentityEvidence(got) {
		t.Errorf("IDENTITY_EVIDENCE = %q, which this client refuses", got)
	}
	for name, want := range map[string]int{
		"MAX_LAN_ADDRESSES": MaxAdvertisedLANAddresses,
		"MAX_WAN_ADDRESSES": MaxObservedWANAddresses,
		"MAX_PEERS":         maxDirectoryPeers,
	} {
		if got := num(name); got != itoaTest(want) {
			t.Errorf("%s = %s, client uses %d", name, got, want)
		}
	}
	// The response fields the client must declare, because DisallowUnknownFields
	// turns an undeclared one into a total failure of the call.
	for _, field := range []string{"ok", "fingerprint", "lanAddresses",
		"observedWanAddress", "identityEvidence", "capabilityEvidence", "authorization"} {
		if !strings.Contains(advertiseBody, `"`+field+`"`) {
			t.Errorf("advertise fixture is missing the %q field", field)
		}
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
