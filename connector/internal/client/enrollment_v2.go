package client

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const EnrollmentSchemaVersion = 2

var manualPairingCode = regexp.MustCompile(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`)

type EnrollmentSessionRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	DeviceName    string `json:"deviceName"`
	DeviceID      string `json:"deviceId"`
	PublicKey     string `json:"publicKey"`
	Platform      string `json:"platform"`
	Architecture  string `json:"architecture"`
}

type EnrollmentSession struct {
	SchemaVersion int    `json:"schemaVersion"`
	SessionID     string `json:"sessionId"`
	UniversalLink string `json:"universalLink"`
	ManualCode    string `json:"manualCode"`
	ExpiresAt     string `json:"expiresAt"`
	Status        string `json:"status"`
	PollToken     string `json:"pollToken"`
}

type EnrollmentSessionStatus struct {
	SchemaVersion  int    `json:"schemaVersion"`
	SessionID      string `json:"sessionId"`
	Status         string `json:"status"`
	Step           string `json:"step"`
	AccountID      string `json:"accountId,omitempty"`
	NetworkID      string `json:"networkId,omitempty"`
	DeviceID       string `json:"deviceId,omitempty"`
	Credential     string `json:"credential,omitempty"`
	HostID         string `json:"hostId,omitempty"`
	HostCredential string `json:"hostCredential,omitempty"`
	ExpiresAt      string `json:"expiresAt"`
}

func (s EnrollmentSession) Validate(coordinator string) error {
	if s.SchemaVersion != EnrollmentSchemaVersion || !canonicalUUID.MatchString(s.SessionID) || !manualPairingCode.MatchString(s.ManualCode) || len(s.PollToken) < 32 || strings.ContainsAny(s.PollToken, " \r\n\t") {
		return errors.New("invalid enrollment session")
	}
	u, err := url.Parse(s.UniversalLink)
	base, baseErr := url.Parse(coordinator)
	ownedLinkHost := strings.EqualFold(u.Host, "link.nexal.systems") || strings.EqualFold(u.Host, base.Host)
	if err != nil || baseErr != nil || u.Scheme != "https" || !ownedLinkHost ||
		!strings.HasPrefix(u.EscapedPath(), "/pair/") || u.RawQuery != "" || !validDigest(u.Fragment) || u.User != nil {
		return errors.New("invalid enrollment universal link")
	}
	if s.Status != "waiting" {
		return errors.New("new enrollment session is not waiting")
	}
	expires, err := time.Parse(time.RFC3339Nano, s.ExpiresAt)
	if err != nil || time.Until(expires) <= 0 || time.Until(expires) > 10*time.Minute {
		return errors.New("invalid enrollment expiry")
	}
	return nil
}

func (c *Client) CreateEnrollmentSession(ctx context.Context, in EnrollmentSessionRequest) (EnrollmentSession, error) {
	var out EnrollmentSession
	if in.SchemaVersion != EnrollmentSchemaVersion || strings.TrimSpace(in.DeviceName) == "" || len(in.DeviceName) > 80 ||
		!validDigest(in.DeviceID) || in.PublicKey == "" || in.Platform != "darwin" || in.Architecture == "" {
		return out, errors.New("invalid enrollment session request")
	}
	if err := c.call(ctx, "POST", "/api/v2/enrollment-sessions", in, &out); err != nil {
		return EnrollmentSession{}, err
	}
	if err := out.Validate(c.base); err != nil {
		return EnrollmentSession{}, err
	}
	return out, nil
}

func (c *Client) EnrollmentSessionState(ctx context.Context, id string) (EnrollmentSessionStatus, error) {
	var out EnrollmentSessionStatus
	if !canonicalUUID.MatchString(id) {
		return out, errors.New("invalid enrollment session id")
	}
	if err := c.call(ctx, "GET", "/api/v2/enrollment-sessions/"+url.PathEscape(id), nil, &out); err != nil {
		return EnrollmentSessionStatus{}, err
	}
	if out.SchemaVersion != EnrollmentSchemaVersion || out.SessionID != id {
		return EnrollmentSessionStatus{}, errors.New("invalid enrollment status")
	}
	switch out.Status {
	case "waiting", "claimed", "authorizing", "provisioning", "joining", "paired", "cancelled", "expired", "failed":
	default:
		return EnrollmentSessionStatus{}, errors.New("unknown enrollment status")
	}
	if out.Status == "paired" && (!ValidID(out.AccountID) || !ValidID(out.NetworkID) || !validDigest(out.DeviceID) || len(out.Credential) < 16 || !ValidID(out.HostID) || len(out.HostCredential) < 16) {
		return EnrollmentSessionStatus{}, errors.New("paired enrollment is incomplete")
	}
	return out, nil
}

func (c *Client) CancelEnrollmentSession(ctx context.Context, id string) error {
	if !canonicalUUID.MatchString(id) {
		return errors.New("invalid enrollment session id")
	}
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := c.call(ctx, "POST", "/api/v2/enrollment-sessions/"+url.PathEscape(id)+"/cancel", struct{}{}, &out); err != nil {
		return err
	}
	if !out.Cancelled {
		return errors.New("coordinator did not confirm enrollment cancellation")
	}
	return nil
}

func validDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
