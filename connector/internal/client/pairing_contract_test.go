package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The pairing contract is fixed at BOTH ends and this file is the middle of it.
// Two kinds of test live here, for the reason HARDENING-PLAN §43 records:
//
//  1. Fixtures that are the coordinator's LITERAL response bodies, copied from
//     apps/coordinator/src/home.ts rather than authored to match the Go structs.
//     A fixture written on the same side as the code under test cannot detect
//     drift, which is how §43's bug survived two green suites.
//  2. Drift tests that READ the platform and iOS sources directly and fail when a
//     constant there stops matching a constant here — the same technique as
//     TestHonestyStringsAndCapsMatchCoordinatorSource. They skip when the sibling
//     repositories are not checked out, so this package still tests standalone.
//
// No test contacts a real coordinator. Everything is httptest.

// testClaimToken has the shape PairingClaim.isValidToken requires: exactly 64
// lowercase hex characters. It is a fixture, not a credential.
const testClaimToken = "7c9e66797c9e66797c9e66797c9e66797c9e66797c9e66797c9e66797c9e6679" // gitleaks:allow -- 64 hex test fixture, not a real token

const testPairingID = "3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"
const testHostID = "host_01HZX3QK7T"

// pairingBody is the LITERAL 201 body of the POST branch in home.ts:
//
//	return reply({ schemaVersion: 1, type: "nexal-device-pairing",
//	  coordinator: new URL(request.url).origin, pairingId, expiresAt: row.expires_at,
//	  role: input.role, claimToken }, 201);
//
// with expires_at in the form the Worker's
// strftime('%Y-%m-%dT%H:%M:%fZ','now','+5 minutes') produces.
const pairingBody = `{
  "schemaVersion": 1,
  "type": "nexal-device-pairing",
  "coordinator": "%s",
  "pairingId": "%s",
  "expiresAt": "%s",
  "role": "%s",
  "claimToken": "%s"
}`

// statusBody is the LITERAL body of the GET branch:
//
//	return reply({ pairingId: row.id, status, expiresAt: row.expires_at });
const statusBody = `{"pairingId":"%s","status":"%s","expiresAt":"%s"}`

// cancelBody is the LITERAL body of the cancel branch: reply({ cancelled: true }).
const cancelBody = `{"cancelled":true}`

// mustReplace fails the test if the substitution found nothing. A drift case
// whose replacement silently no-ops degrades into a second copy of the control
// case: it asserts the OPPOSITE of what it claims to, and reports that as a
// failure of the code under test rather than of itself.
func mustReplace(t *testing.T, body, old, replacement string) string {
	t.Helper()
	if !strings.Contains(body, old) {
		t.Fatalf("drift case is broken: %q is not present in the control body", old)
	}
	return strings.Replace(body, old, replacement, 1)
}

func futureExpiry(d time.Duration) string {
	// The Worker's %f gives milliseconds; match that, not Go's default precision.
	return time.Now().UTC().Add(d).Format("2006-01-02T15:04:05.000Z")
}

// pairingServer serves one handler and records what the client sent.
type recorded struct {
	method, path string
	body         map[string]any
	rawBody      string
	auth         string
}

func pairingServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec *recorded)) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path, rec.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(r.Body)
		rec.rawBody = raw.String()
		_ = json.Unmarshal(raw.Bytes(), &rec.body)
		w.Header().Set("Content-Type", "application/json")
		handler(w, r, rec)
	}))
	t.Cleanup(srv.Close)
	// dev=true: httptest serves plain HTTP, which ValidateURL refuses otherwise.
	c, err := New(srv.URL, "t_0123456789abcdef", true)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c, rec
}

func mintAgainst(t *testing.T, role string, body func(origin string) string, code int) (Pairing, *recorded, error) {
	t.Helper()
	c, rec := pairingServer(t, func(w http.ResponseWriter, r *http.Request, _ *recorded) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body(strings.TrimRight(r.Host, "/"))))
	})
	p, err := c.CreatePairing(context.Background(), testHostID, role)
	return p, rec, err
}

func TestCreatePairingAcceptsTheRealCoordinatorResponse(t *testing.T) {
	expiry := futureExpiry(5 * time.Minute)
	c, rec := pairingServer(t, func(w http.ResponseWriter, r *http.Request, _ *recorded) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte(fmt.Sprintf(pairingBody, "http://"+r.Host, testPairingID, expiry, "receiver", testClaimToken)))
	})
	p, err := c.CreatePairing(context.Background(), testHostID, PairingRoleReceiver)
	if err != nil {
		t.Fatalf("real coordinator pairing response rejected: %v", err)
	}
	if p.SchemaVersion != 1 || p.Type != PairingType || p.PairingID != testPairingID ||
		p.Role != "receiver" || p.ExpiresAt != expiry || p.ClaimToken != testClaimToken {
		t.Fatalf("pairing fields dropped: %s", p)
	}
	// The route the coordinator's regex matches, with the FULL host id: requireHost
	// compares the path segment against host.id, so a trimmed "device id" 401s.
	if rec.method != "POST" || rec.path != "/api/home/v1/devices/"+testHostID+"/pairings" {
		t.Fatalf("minted with %s %s", rec.method, rec.path)
	}
	// keys(input, ["role"]) rejects any other member, so the body is exactly one key.
	if len(rec.body) != 1 || rec.body["role"] != "receiver" {
		t.Fatalf("request body = %v", rec.body)
	}
	// requireHost reads a bearer token: the enrolled HOST credential, not an owner
	// token and not a phone session.
	if !strings.HasPrefix(rec.auth, "Bearer ") {
		t.Fatalf("pairing was not authenticated as a host")
	}
}

// TestPairingPayloadIsExactlyWhatPairingPayloadSwiftAccepts is the most important
// test in this file. It encodes the payload the QR carries and drives it through a
// Go re-implementation of the phone's parser rules, then asserts the literal byte
// string. A drift here is a phone that silently refuses every code.
func TestPairingPayloadIsExactlyWhatPairingPayloadSwiftAccepts(t *testing.T) {
	expiry := futureExpiry(5 * time.Minute)
	p := Pairing{SchemaVersion: 1, Type: PairingType, Coordinator: "https://coordinator-dev.nexal.systems",
		PairingID: testPairingID, ExpiresAt: expiry, Role: PairingRoleReceiver, ClaimToken: testClaimToken}
	payload, err := p.Payload()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schemaVersion":1,"type":"nexal-device-pairing","coordinator":"https://coordinator-dev.nexal.systems",` +
		`"pairingId":"` + testPairingID + `","expiresAt":"` + expiry + `","role":"receiver","claimToken":"` + testClaimToken + `"}`
	if string(payload) != want {
		t.Fatalf("payload bytes differ from the contract\n got %s\nwant %s", payload, want)
	}
	fields, err := parseFlatJSONLikeThePhone(payload)
	if err != nil {
		t.Fatalf("the phone's parser rules reject this payload: %v", err)
	}
	// guard Set(fields.keys) == expected — exactly these seven, no more, no fewer.
	expected := map[string]bool{"schemaVersion": true, "type": true, "coordinator": true,
		"pairingId": true, "expiresAt": true, "role": true, "claimToken": true}
	if len(fields) != len(expected) {
		t.Fatalf("payload has %d keys, the phone requires exactly %d", len(fields), len(expected))
	}
	for key := range fields {
		if !expected[key] {
			t.Fatalf("payload carries %q, which the phone rejects as an unexpected key", key)
		}
	}
	// guard case .integer(let version) = fields["schemaVersion"] — an integer, so
	// "1" or 1.0 would both fail. Go's encoder writes 1; assert it stayed an int.
	if !regexp.MustCompile(`"schemaVersion":1[,}]`).Match(payload) {
		t.Error("schemaVersion must be encoded as the integer 1, not a string or a float")
	}
	// guard text.utf8.count <= 2048, !text.isEmpty
	if len(payload) == 0 || len(payload) > 2048 {
		t.Errorf("payload is %d bytes; the phone accepts 1..2048", len(payload))
	}
	// The payload must contain no whitespace outside string values: the phone's
	// parser tolerates it, but an encoder that starts pretty-printing has silently
	// grown the payload and the symbol version with it.
	if bytes.ContainsAny(payload, "\n\t\r") {
		t.Error("payload contains structural whitespace")
	}
}

// parseFlatJSONLikeThePhone mirrors FlatJSONObject in PairingPayload.swift: a flat
// object of string and integer members only, duplicate keys rejected, nothing
// permitted after the closing brace. It is intentionally a re-implementation
// rather than a call to encoding/json, because encoding/json accepts duplicate
// keys, nesting, booleans, floats and null — the exact latitude the phone's
// parser was written to refuse.
func parseFlatJSONLikeThePhone(b []byte) (map[string]any, error) {
	i := 0
	skip := func() {
		for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
			i++
		}
	}
	str := func() (string, error) {
		start := i
		if i >= len(b) || b[i] != '"' {
			return "", fmt.Errorf("expected a string at byte %d", i)
		}
		i++
		for i < len(b) {
			c := b[i]
			i++
			if c == '\\' {
				if i >= len(b) {
					return "", fmt.Errorf("truncated escape at byte %d", i)
				}
				i++
				continue
			}
			if c == '"' {
				var out string
				if err := json.Unmarshal(b[start:i], &out); err != nil {
					return "", err
				}
				return out, nil
			}
		}
		return "", fmt.Errorf("unterminated string at byte %d", start)
	}
	skip()
	if i >= len(b) || b[i] != '{' {
		return nil, fmt.Errorf("payload is not a JSON object")
	}
	i++
	skip()
	if i < len(b) && b[i] == '}' {
		return nil, fmt.Errorf("an empty object is refused")
	}
	fields := map[string]any{}
	for {
		key, err := str()
		if err != nil {
			return nil, err
		}
		if _, seen := fields[key]; seen {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		skip()
		if i >= len(b) || b[i] != ':' {
			return nil, fmt.Errorf("expected ':' at byte %d", i)
		}
		i++
		skip()
		if i < len(b) && b[i] == '"' {
			value, err := str()
			if err != nil {
				return nil, err
			}
			fields[key] = value
		} else {
			start := i
			if i < len(b) && b[i] == '-' {
				i++
			}
			if i >= len(b) || b[i] < '0' || b[i] > '9' {
				return nil, fmt.Errorf("value of %q is neither a string nor an integer", key)
			}
			if b[i] == '0' {
				i++
			} else {
				for i < len(b) && b[i] >= '0' && b[i] <= '9' {
					i++
				}
			}
			var value int
			if err := json.Unmarshal(b[start:i], &value); err != nil {
				return nil, err
			}
			fields[key] = value
		}
		skip()
		if i < len(b) && b[i] == '}' {
			i++
			break
		}
		if i >= len(b) || b[i] != ',' {
			return nil, fmt.Errorf("expected ',' at byte %d", i)
		}
		i++
		skip()
	}
	skip()
	if i != len(b) {
		return nil, fmt.Errorf("%d trailing bytes", len(b)-i)
	}
	return fields, nil
}

// TestPairingContractMatchesIOSSource reads the phone's parser and asserts the
// constants this package mirrors are still the ones it enforces. It fails, rather
// than silently diverging, if PairingPayload.swift or Models.swift changes.
func TestPairingContractMatchesIOSSource(t *testing.T) {
	payloadSource := readSibling(t, "../../../../nexal-ios/Sources/NexalHomeCore/PairingPayload.swift")
	models := readSibling(t, "../../../../nexal-ios/Sources/NexalHomeCore/Models.swift")
	// The expected key set, taken from the phone's own declaration.
	keys := regexp.MustCompile(`let expected: Set<String> = \[([^\]]+)\]`).FindStringSubmatch(payloadSource)
	if keys == nil {
		t.Fatal("could not find the expected key set in PairingPayload.swift")
	}
	want := map[string]bool{}
	for _, field := range strings.Split(keys[1], ",") {
		want[strings.Trim(strings.TrimSpace(field), `"`)] = true
	}
	var p Pairing
	payload, err := Pairing{SchemaVersion: 1, Type: PairingType, Coordinator: "https://x.test",
		PairingID: testPairingID, ExpiresAt: futureExpiry(time.Minute), Role: PairingRoleDonor,
		ClaimToken: testClaimToken}.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("payload has keys %v, the phone expects %v", keysOf(got), keysOf(want))
	}
	for key := range got {
		if !want[key] {
			t.Errorf("payload key %q is not in the phone's expected set", key)
		}
	}
	// The type discriminator, verbatim.
	if !strings.Contains(payloadSource, `.string("`+PairingType+`")`) {
		t.Errorf("PairingPayload.swift no longer requires type == %q", PairingType)
	}
	// PairingClaim.isValidToken: 64 bytes, digits and lowercase a-f.
	claim := regexp.MustCompile(`token\.utf8\.count == (\d+) && token\.utf8\.allSatisfy \{ \((\d+)\.\.\.(\d+)\)\.contains\(\$0\) \|\| \((\d+)\.\.\.(\d+)\)\.contains\(\$0\) \}`).FindStringSubmatch(models)
	if claim == nil {
		t.Fatal("could not find PairingClaim.isValidToken in Models.swift")
	}
	if claim[1] != "64" || claim[2] != "48" || claim[3] != "57" || claim[4] != "97" || claim[5] != "102" {
		t.Fatalf("the phone's claim token rule changed to %v; claimTokenShape must be updated", claim[1:])
	}
	// The five-minute freshness bound the phone enforces on expiresAt.
	if !strings.Contains(payloadSource, "expiry.timeIntervalSince(now) <= 300") {
		t.Error("PairingPayload.swift no longer bounds the pairing lifetime at 300 seconds")
	}
	if PairingTTL != 300*time.Second {
		t.Errorf("PairingTTL is %v, the phone allows at most 300s", PairingTTL)
	}
	// DeviceRole's two cases.
	if !regexp.MustCompile(`enum DeviceRole: String[^\n]*case donor, receiver`).MatchString(models) {
		t.Error("DeviceRole is no longer exactly {donor, receiver}")
	}
	// CoordinatorOrigin accepts https only, which is why a loopback development
	// configuration cannot pair; Validate says so and this keeps that honest.
	if !strings.Contains(models, `c.scheme?.lowercased() == "https"`) {
		t.Error("CoordinatorOrigin no longer requires https; the pairing error message must be revisited")
	}
}

// TestPairingRoutesMatchCoordinatorSource pins the three routes, the host
// authentication, the server-side TTL and the four status values against
// apps/coordinator/src/home.ts.
func TestPairingRoutesMatchCoordinatorSource(t *testing.T) {
	source := readSibling(t, "../../../../nexal-platform/apps/coordinator/src/home.ts")
	for _, fragment := range []string{
		// The route regex: /api/home/v1/devices/{id}/pairings[/{uuid}[/cancel]]
		`/^\/api\/home\/v1\/devices\/([A-Za-z0-9_-]{1,80})\/pairings(?:\/([a-f0-9-]{36})(\/cancel)?)?$/`,
		// requireHost, i.e. the enrolled host credential, checked against the path.
		`requireHost(request, env, deviceRoute[1])`,
		// The request body is exactly {"role": ...}.
		`keys(input, ["role"])`,
		`["donor", "receiver"].includes(String(input.role))`,
		// The response key set.
		`schemaVersion: 1, type: "nexal-device-pairing"`,
		// The server-owned lifetime.
		`'+5 minutes'`,
		// Cancel takes an empty object.
		`keys(await readJson(request), [])`,
		`reply({ cancelled: true })`,
	} {
		if !strings.Contains(source, fragment) {
			t.Errorf("home.ts no longer contains %s", fragment)
		}
	}
	// The status derivation, and therefore the four values this client accepts.
	if !strings.Contains(source, `row.cancelled ? "cancelled" : row.expires_at <= iso() ? "expired" :`) ||
		!strings.Contains(source, `row.account_id ? "scanned" : "waiting"`) {
		t.Error("home.ts's pairing status derivation changed; PairingState's accepted values must be updated")
	}
}

func TestPairingStatusAcceptsEveryLiteralStatusBody(t *testing.T) {
	expiry := futureExpiry(4 * time.Minute)
	for _, status := range []string{PairingWaiting, PairingScanned, PairingCancelled, PairingExpired} {
		c, rec := pairingServer(t, func(w http.ResponseWriter, _ *http.Request, _ *recorded) {
			_, _ = w.Write([]byte(fmt.Sprintf(statusBody, testPairingID, status, expiry)))
		})
		got, err := c.PairingState(context.Background(), testHostID, testPairingID)
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if got.Status != status || got.ExpiresAt != expiry {
			t.Errorf("%s decoded as %+v", status, got)
		}
		if rec.method != "GET" || rec.path != "/api/home/v1/devices/"+testHostID+"/pairings/"+testPairingID {
			t.Errorf("%s read with %s %s", status, rec.method, rec.path)
		}
		if PairingTerminal(status) == (status == PairingWaiting) {
			t.Errorf("PairingTerminal(%s) is wrong", status)
		}
	}
	// A status this connector does not understand must fail rather than being
	// displayed as if it were understood.
	c, _ := pairingServer(t, func(w http.ResponseWriter, _ *http.Request, _ *recorded) {
		_, _ = w.Write([]byte(fmt.Sprintf(statusBody, testPairingID, "linked", expiry)))
	})
	if _, err := c.PairingState(context.Background(), testHostID, testPairingID); err == nil {
		t.Error("an unknown pairing status was accepted")
	}
}

func TestCancelPairingSendsAnEmptyObjectAndRequiresConfirmation(t *testing.T) {
	c, rec := pairingServer(t, func(w http.ResponseWriter, _ *http.Request, _ *recorded) {
		_, _ = w.Write([]byte(cancelBody))
	})
	if err := c.CancelPairing(context.Background(), testHostID, testPairingID); err != nil {
		t.Fatalf("cancel rejected: %v", err)
	}
	if rec.method != "POST" || rec.path != "/api/home/v1/devices/"+testHostID+"/pairings/"+testPairingID+"/cancel" {
		t.Fatalf("cancelled with %s %s", rec.method, rec.path)
	}
	// keys(await readJson(request), []) requires a body that is an empty object.
	if strings.TrimSpace(rec.rawBody) != "{}" {
		t.Fatalf("cancel body = %q, the coordinator requires {}", rec.rawBody)
	}
	c2, _ := pairingServer(t, func(w http.ResponseWriter, _ *http.Request, _ *recorded) {
		_, _ = w.Write([]byte(`{"cancelled":false}`))
	})
	if err := c2.CancelPairing(context.Background(), testHostID, testPairingID); err == nil {
		t.Error("an unconfirmed cancellation was reported as success")
	}
}

// TestCreatePairingRefusesDriftedPayloads is the fail-closed half: each case is a
// response the phone would reject, and this client must reject it FIRST, on the
// Mac, where the reason can be shown — rather than drawing a code that fails
// silently in a camera viewfinder.
func TestCreatePairingRefusesDriftedPayloads(t *testing.T) {
	// The control expiry is computed ONCE and reused as both the body value and
	// the search string. Calling futureExpiry twice races the millisecond in the
	// format: when the clock ticks between the two calls the strings differ,
	// strings.Replace matches nothing, and the case silently tests the UNMODIFIED
	// control body -- which is valid, so the client accepts it and the drift case
	// reports a false failure. That flake is far likelier under -race.
	controlExpiry := futureExpiry(5 * time.Minute)
	good := func(origin string) string {
		return fmt.Sprintf(pairingBody, "http://"+origin, testPairingID, controlExpiry, "receiver", testClaimToken)
	}
	if _, _, err := mintAgainst(t, PairingRoleReceiver, good, 201); err != nil {
		t.Fatalf("the control case must be accepted: %v", err)
	}
	cases := map[string]func(origin string) string{
		"an extra field the phone would reject": func(o string) string {
			return strings.Replace(good(o), `"role": "receiver"`, `"role": "receiver",
  "capabilities": "none"`, 1)
		},
		"a missing field": func(o string) string {
			return strings.Replace(good(o), `"role": "receiver",`, "", 1)
		},
		"an uppercase UUID": func(o string) string {
			return strings.Replace(good(o), testPairingID, strings.ToUpper(testPairingID), 1)
		},
		"a non-canonical UUID": func(o string) string {
			return strings.Replace(good(o), testPairingID, "3f2a1b4c5d6e4f708a9b0c1d2e3f4a5b", 1)
		},
		"a short claim token": func(o string) string {
			return strings.Replace(good(o), testClaimToken, testClaimToken[:63], 1)
		},
		"a non-hex claim token": func(o string) string {
			return strings.Replace(good(o), testClaimToken, strings.Repeat("z", 64), 1)
		},
		"an uppercase claim token": func(o string) string {
			return strings.Replace(good(o), testClaimToken, strings.ToUpper(testClaimToken), 1)
		},
		"a local-time expiry": func(o string) string {
			return mustReplace(t, good(o), controlExpiry, "2027-01-01T00:00:00+01:00")
		},
		"an already-past expiry": func(o string) string {
			return mustReplace(t, good(o), controlExpiry, futureExpiry(-time.Minute))
		},
		"an expiry beyond the five-minute contract": func(o string) string {
			return mustReplace(t, good(o), controlExpiry, futureExpiry(2*time.Hour))
		},
		"the wrong schema version": func(o string) string {
			return strings.Replace(good(o), `"schemaVersion": 1`, `"schemaVersion": 2`, 1)
		},
		"a schema version encoded as a string": func(o string) string {
			return strings.Replace(good(o), `"schemaVersion": 1`, `"schemaVersion": "1"`, 1)
		},
		"the wrong type discriminator": func(o string) string {
			return strings.Replace(good(o), PairingType, "nexal-pairing", 1)
		},
		"a role other than the one requested": func(o string) string {
			return strings.Replace(good(o), `"role": "receiver"`, `"role": "donor"`, 1)
		},
		"a coordinator origin that is not this connector's": func(o string) string {
			return strings.Replace(good(o), "http://"+o, "http://coordinator.invalid", 1)
		},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p, _, err := mintAgainst(t, PairingRoleReceiver, body, 201)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			// A refused pairing must not leave a usable claim token behind.
			if p.ClaimToken != "" {
				t.Error("a rejected pairing returned a claim token")
			}
			if strings.Contains(err.Error(), testClaimToken) {
				t.Error("the error message leaked the claim token")
			}
		})
	}
}

// TestPairingStatusCodesExplainThemselves checks §26's requirement that an
// unavailable feature says WHICH thing is unavailable. The 503 in particular is
// the coordinator's FEATURE_HOME_PAIRING gate, which is a configuration fact the
// owner can act on, not a network failure.
func TestPairingStatusCodesExplainThemselves(t *testing.T) {
	for status, fragment := range map[int]string{
		401: "host credential",
		403: "https coordinator",
		503: "not enabled on this coordinator",
	} {
		c, _ := pairingServer(t, func(w http.ResponseWriter, _ *http.Request, _ *recorded) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"home_not_configured","message":"Phone pairing is not enabled."}}`))
		})
		_, err := c.CreatePairing(context.Background(), testHostID, PairingRoleDonor)
		if err == nil || !strings.Contains(err.Error(), fragment) {
			t.Errorf("HTTP %d produced %v, expected a message mentioning %q", status, err, fragment)
		}
		if err != nil && strings.Contains(err.Error(), "http://127.0.0.1") {
			t.Errorf("HTTP %d error leaked the coordinator URL", status)
		}
	}
}

// TestClaimTokenNeverAppearsInFormattingOrLogs is the secrecy assertion the
// comment on Pairing.ClaimToken promises. The token is legitimate inside the QR
// modules and nowhere else, so the ways a secret usually escapes — a %v in a
// debug print, a structured log field — are closed here rather than by convention.
func TestClaimTokenNeverAppearsInFormattingOrLogs(t *testing.T) {
	p := Pairing{SchemaVersion: 1, Type: PairingType, Coordinator: "https://x.test",
		PairingID: testPairingID, ExpiresAt: futureExpiry(time.Minute),
		Role: PairingRoleDonor, ClaimToken: testClaimToken}
	// %#v is deliberately absent: Go-syntax formatting bypasses Stringer and no
	// amount of care here can stop it, which is why no code path in this repository
	// formats a Pairing that way and why the struct is never embedded in one that
	// might be dumped wholesale.
	for _, format := range []string{"%v", "%s", "%+v"} {
		rendered := fmt.Sprintf(format, p)
		if strings.Contains(rendered, testClaimToken) {
			t.Errorf("fmt %s leaked the claim token", format)
		}
		if !strings.Contains(rendered, "[redacted]") {
			t.Errorf("fmt %s did not redact", format)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("pairing", "pairing", p)
	if strings.Contains(buf.String(), testClaimToken) {
		t.Error("slog leaked the claim token")
	}
	if !strings.Contains(buf.String(), testPairingID) {
		t.Error("slog dropped the pairing id, so the record is useless")
	}
	// The one place it must appear: the payload that becomes the QR.
	payload, err := p.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), testClaimToken) {
		t.Error("the QR payload must carry the claim token; that is what the phone claims with")
	}
}

func TestPairingRejectsBadArgumentsBeforeAnyRequest(t *testing.T) {
	c, rec := pairingServer(t, func(w http.ResponseWriter, _ *http.Request, _ *recorded) {
		w.WriteHeader(500)
	})
	ctx := context.Background()
	if _, err := c.CreatePairing(ctx, testHostID, "observer"); err == nil {
		t.Error("an unknown role was sent to the coordinator")
	}
	if _, err := c.CreatePairing(ctx, "host id with spaces", PairingRoleDonor); err == nil {
		t.Error("an invalid host id was sent to the coordinator")
	}
	if _, err := c.PairingState(ctx, testHostID, "not-a-uuid"); err == nil {
		t.Error("an invalid pairing id was sent to the coordinator")
	}
	if err := c.CancelPairing(ctx, testHostID, strings.ToUpper(testPairingID)); err == nil {
		t.Error("an uppercase pairing id was sent to the coordinator")
	}
	if rec.method != "" {
		t.Errorf("a request was made despite invalid arguments: %s %s", rec.method, rec.path)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// readSibling loads a file from a sibling repository, skipping the test when the
// repository is not checked out alongside this one. Skipping is correct here: the
// connector must build and test standalone, and a drift test that fails for a
// missing checkout would be turned off rather than fixed.
func readSibling(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("sibling repository not checked out (%s); pairing contract drift is unchecked in this run", path)
	}
	return string(b)
}
