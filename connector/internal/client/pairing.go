package client

// Phone pairing, host side. This is the middle of a contract that is ALREADY
// FIXED at both ends: `apps/coordinator/src/home.ts` mints the pairing and the
// iOS app's `PairingPayload.parse` consumes the QR. Nothing here may be invented,
// and the validation below is deliberately a mirror of the phone's parser rather
// than a looser "it looks fine" check — a payload this connector accepts but the
// phone rejects is a code that silently never scans, which is the single worst
// outcome for this feature.
//
// AUTHENTICATION: these three routes are behind the coordinator's `requireHost`,
// i.e. the enrolled HOST credential this connector already holds. Not the owner
// token, not a phone's home session. `requireHost(request, env, deviceRoute[1])`
// compares the path segment against `host.id`, so the device id in the path is
// this host's FULL id (`config.HostID`), not a trimmed form of it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"context"
)

// PairingRoleDonor and PairingRoleReceiver are the only two roles the coordinator
// accepts (`["donor","receiver"].includes(...)` in home.ts) and the only two the
// phone's DeviceRole enum decodes.
const (
	PairingRoleDonor    = "donor"
	PairingRoleReceiver = "receiver"
)

// PairingType is the discriminator the phone requires verbatim.
const PairingType = "nexal-device-pairing"

// PairingSchemaVersion is the only schema version the phone accepts; a different
// value makes it report `unsupportedVersion` rather than a scan failure.
const PairingSchemaVersion = 1

// PairingTTL is the coordinator's pairing lifetime ('+5 minutes' in the INSERT).
// It is used ONLY as a sanity bound on a returned expiresAt; the real deadline
// always comes from the coordinator's own value, because a clock skew or a
// changed server-side TTL must not be papered over by a constant here.
const PairingTTL = 5 * time.Minute

// Pairing is the coordinator's 201 response, and byte for byte the object that
// goes into the QR. The json tags and the field set are the QR CONTRACT: the
// phone requires EXACTLY these seven keys, so a field added here (even an
// innocuous one) breaks every scan. Field order is the order the payload is
// serialized in, which the phone does not care about but a human comparing a
// dump to home.ts does.
type Pairing struct {
	SchemaVersion int    `json:"schemaVersion"`
	Type          string `json:"type"`
	Coordinator   string `json:"coordinator"`
	PairingID     string `json:"pairingId"`
	ExpiresAt     string `json:"expiresAt"`
	Role          string `json:"role"`
	// ClaimToken authorizes a phone to claim this Mac. It is a SECRET: it is
	// never logged, never persisted to the configuration file, never placed in an
	// error message, and never emitted as a readable JSON field by the CLI. It
	// lives in this process's memory and in the rendered QR, and nowhere else.
	// String() and LogValue() below exist so that an accidental %v or slog call
	// cannot leak it; TestClaimTokenNeverAppearsInFormattingOrLogs asserts it.
	ClaimToken string `json:"claimToken"`
}

// String redacts. A pairing printed with %v, %s or %+v must not reveal the claim
// token, because the most likely way a secret escapes is a debug print that was
// never meant to survive.
func (p Pairing) String() string {
	return fmt.Sprintf("pairing{id:%s role:%s expiresAt:%s claimToken:[redacted]}",
		p.PairingID, p.Role, p.ExpiresAt)
}

// LogValue keeps the same redaction when a Pairing reaches slog.
func (p Pairing) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("pairingId", p.PairingID),
		slog.String("role", p.Role),
		slog.String("expiresAt", p.ExpiresAt),
	)
}

// PairingStatus is the GET response. Status is one of waiting, scanned,
// cancelled or expired — derived server-side, so this connector never computes
// "expired" from its own clock and reports it as if the coordinator had said so.
type PairingStatus struct {
	PairingID string `json:"pairingId"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expiresAt"`
}

// The four status values home.ts can produce.
const (
	PairingWaiting   = "waiting"
	PairingScanned   = "scanned"
	PairingCancelled = "cancelled"
	PairingExpired   = "expired"
)

// PairingTerminal reports whether a status ends the poll. Anything other than
// "waiting" is terminal: scanned is success, and the other two mean this pairing
// will never become scanned.
func PairingTerminal(status string) bool { return status != PairingWaiting }

// canonicalUUID is the phone's isCanonicalUUID with the case latitude removed.
// The phone accepts upper case; the coordinator's crypto.randomUUID always emits
// lower case, so this connector requires lower case and treats anything else as
// a contract change worth failing on rather than normalising silently.
var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// claimTokenShape is PairingClaim.isValidToken: exactly 64 lowercase hex bytes.
var claimTokenShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// wireDate is WireDate.parse's regular expression: RFC3339, UTC only, optional
// fractional seconds. The coordinator emits the three-digit millisecond form.
var wireDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$`)

// CreatePairing mints a pairing for this host in the given role.
func (c *Client) CreatePairing(ctx context.Context, deviceID, role string) (Pairing, error) {
	var out Pairing
	if !ValidID(deviceID) {
		return out, errors.New("invalid host id")
	}
	if role != PairingRoleDonor && role != PairingRoleReceiver {
		return out, errors.New("pairing role must be donor or receiver")
	}
	err := c.call(ctx, "POST", "/api/home/v1/devices/"+url.PathEscape(deviceID)+"/pairings",
		struct {
			Role string `json:"role"`
		}{role}, &out)
	if err != nil {
		return Pairing{}, pairingError(err)
	}
	if err := out.Validate(c.base, role, c.dev); err != nil {
		// Zero the struct so a rejected payload cannot be rendered by a caller that
		// ignores the error, and so the claim token does not outlive the failure.
		return Pairing{}, err
	}
	return out, nil
}

// PairingState reads one pairing's current status.
func (c *Client) PairingState(ctx context.Context, deviceID, pairingID string) (PairingStatus, error) {
	var out PairingStatus
	if !ValidID(deviceID) {
		return out, errors.New("invalid host id")
	}
	if !canonicalUUID.MatchString(pairingID) {
		return out, errors.New("pairing id must be a lowercase canonical UUID")
	}
	err := c.call(ctx, "GET", "/api/home/v1/devices/"+url.PathEscape(deviceID)+"/pairings/"+pairingID, nil, &out)
	if err != nil {
		return PairingStatus{}, pairingError(err)
	}
	if out.PairingID != pairingID {
		return PairingStatus{}, errors.New("coordinator answered about a different pairing")
	}
	switch out.Status {
	case PairingWaiting, PairingScanned, PairingCancelled, PairingExpired:
	default:
		// A status this connector does not understand must not be reported as if it
		// were understood; the UI would show a state it cannot explain.
		return PairingStatus{}, errors.New("coordinator reported an unknown pairing status")
	}
	if !wireDate.MatchString(out.ExpiresAt) {
		return PairingStatus{}, errors.New("coordinator sent an unparseable pairing expiry")
	}
	return out, nil
}

// CancelPairing cancels a pairing. It is idempotent server-side: the UPDATE
// matches nothing for an already-cancelled or unknown pairing and still answers
// {"cancelled":true}, which is why this returns no "was it really cancelled"
// signal to display.
func (c *Client) CancelPairing(ctx context.Context, deviceID, pairingID string) error {
	if !ValidID(deviceID) {
		return errors.New("invalid host id")
	}
	if !canonicalUUID.MatchString(pairingID) {
		return errors.New("pairing id must be a lowercase canonical UUID")
	}
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := c.call(ctx, "POST", "/api/home/v1/devices/"+url.PathEscape(deviceID)+"/pairings/"+pairingID+"/cancel",
		struct{}{}, &out); err != nil {
		return pairingError(err)
	}
	if !out.Cancelled {
		return errors.New("coordinator did not confirm the cancellation")
	}
	return nil
}

// Validate is the phone's parser, re-implemented on this side so a payload the
// phone would refuse never reaches a screen. Every clause corresponds to a guard
// in PairingPayload.parse; the comment names the guard so the two can be diffed
// by hand. The error messages never include the claim token.
// dev relaxes ONLY the https clause, and only because the test harness and the
// loopback profile speak plain HTTP. It does not relax any field rule: a payload
// that would fail on the phone for any other reason still fails here.
func (p Pairing) Validate(coordinator, expectedRole string, dev bool) error {
	if p.SchemaVersion != PairingSchemaVersion {
		return fmt.Errorf("pairing schemaVersion %d is not supported (phone requires %d)", p.SchemaVersion, PairingSchemaVersion)
	}
	if p.Type != PairingType { // fields["type"] == .string("nexal-device-pairing")
		return errors.New("pairing payload type does not match the phone contract")
	}
	if !canonicalUUID.MatchString(p.PairingID) { // isCanonicalUUID
		return errors.New("pairing id is not a canonical lowercase UUID")
	}
	if !claimTokenShape.MatchString(p.ClaimToken) { // PairingClaim.isValidToken
		return errors.New("pairing claim token is not 64 lowercase hex characters")
	}
	if !wireDate.MatchString(p.ExpiresAt) { // WireDate.parse
		return errors.New("pairing expiry is not an RFC3339 UTC timestamp")
	}
	expiry, err := ParsePairingExpiry(p.ExpiresAt)
	if err != nil {
		return err
	}
	// guard expiry > now, expiry.timeIntervalSince(now) <= 300
	if remaining := time.Until(expiry); remaining <= 0 || remaining > PairingTTL {
		return errors.New("pairing expiry is already past or further away than the five-minute contract allows")
	}
	if p.Role != expectedRole { // guard role == expectedRole
		return errors.New("coordinator returned a different pairing role than requested")
	}
	if p.Role != PairingRoleDonor && p.Role != PairingRoleReceiver { // DeviceRole(rawValue:)
		return errors.New("pairing role is not donor or receiver")
	}
	// guard origin == pinnedOrigin. The phone pins ITS coordinator origin and
	// compares canonically, so a mismatch here is a code the phone will refuse.
	if !strings.EqualFold(strings.TrimRight(p.Coordinator, "/"), strings.TrimRight(coordinator, "/")) {
		return errors.New("pairing coordinator origin does not match this connector's configured coordinator")
	}
	// CoordinatorOrigin requires https. The coordinator itself also rejects a
	// non-https request to /api/home/v1/, so a loopback development configuration
	// cannot pair at all; say that plainly instead of drawing a code the phone
	// will reject with wrongCoordinator.
	if !dev && !strings.HasPrefix(strings.ToLower(p.Coordinator), "https://") {
		return errors.New("phone pairing requires an https coordinator; the development loopback profile cannot pair")
	}
	return nil
}

// Payload is the exact byte string that goes into the QR: a flat JSON object
// with the seven contract keys and no whitespace.
//
// It is re-encoded from the parsed struct rather than forwarding the
// coordinator's raw response body, deliberately. The phone's parser is strict
// about the KEY SET (`Set(fields.keys) == expected`) and rejects duplicates,
// nesting and any extra member, so re-encoding from a typed struct guarantees the
// key set regardless of what a future coordinator adds to its response. HTML
// escaping is disabled because Go's encoder would otherwise turn a `&` in an
// origin into `\u0026`; the phone's parser would accept that (it decodes strings
// through JSONDecoder) but the resulting origin would no longer be byte-identical
// to the one it pinned.
func (p Pairing) Payload() ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, errors.New("cannot encode pairing payload")
	}
	return []byte(strings.TrimRight(b.String(), "\n")), nil
}

// ParsePairingExpiry parses the coordinator's timestamp. Both the millisecond
// form the Worker emits and the bare-second form the contract permits are
// accepted, which is the same latitude WireDate.parse allows.
func ParsePairingExpiry(value string) (time.Time, error) {
	if !wireDate.MatchString(value) {
		return time.Time{}, errors.New("pairing expiry is not an RFC3339 UTC timestamp")
	}
	expiry, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("pairing expiry is not a valid timestamp")
	}
	return expiry, nil
}

// pairingError turns the coordinator's status code into the reason an owner can
// act on. §26: the surface must say WHICH thing is unavailable, not just that
// something is.
func pairingError(err error) error {
	var status *StatusError
	if !errors.As(err, &status) {
		return err
	}
	switch status.Status {
	case 401:
		return errors.New("coordinator did not accept this host credential for pairing (HTTP 401): the host may be revoked or its token expired; re-enroll this Mac")
	case 403:
		return errors.New("coordinator refused the pairing (HTTP 403): pairing requires an https coordinator and a live, non-fixture enrolled host")
	case 404:
		return errors.New("coordinator has no such pairing (HTTP 404): it may have expired and been swept, or it belongs to another host")
	case 503:
		return errors.New("phone pairing is not enabled on this coordinator (HTTP 503): its FEATURE_HOME_PAIRING and home account configuration are off")
	default:
		return err
	}
}
