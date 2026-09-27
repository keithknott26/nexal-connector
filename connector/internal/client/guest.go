package client

import (
	"context"
	"errors"
	"nexal/connector/internal/config"
	"strings"
	"time"
)

func NormalizeGuestCode(code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	if len(code) != 8 {
		return "", errors.New("invitation code must contain eight letters or digits")
	}
	for _, r := range code {
		if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZ23456789", r) {
			return "", errors.New("invalid invitation code")
		}
	}
	return code, nil
}
func (c *Client) RedeemGuestInvitation(ctx context.Context, code, sessionID, pollToken string) (EnrollmentSessionStatus, error) {
	var out EnrollmentSessionStatus
	normalized, err := NormalizeGuestCode(code)
	if err != nil {
		return out, err
	}
	if !canonicalUUID.MatchString(sessionID) || len(pollToken) < 32 || len(pollToken) > 4096 || strings.ContainsAny(pollToken, " \r\n\t") {
		return out, errors.New("invalid invitation session")
	}
	input := struct {
		Code      string `json:"code"`
		SessionID string `json:"sessionId"`
		PollToken string `json:"pollToken"`
	}{normalized, sessionID, pollToken}
	if err = c.call(ctx, "POST", "/api/v2/network-invitations/redeem", input, &out); err != nil {
		return out, err
	}
	if err = out.ValidateGuest(sessionID); err != nil {
		return EnrollmentSessionStatus{}, err
	}
	return out, nil
}
func (s EnrollmentSessionStatus) ValidateGuest(sessionID string) error {
	if s.SchemaVersion != 2 || s.SessionID != sessionID || !containsGuestStatus(s.Status) || !ValidID(s.GrantID) || !ValidGuestHostname(s.GrantID, s.MeshHostname) {
		return errors.New("invalid invitation grant")
	}
	serverNow, e1 := time.Parse(time.RFC3339Nano, s.ServerNow)
	deadline, e2 := time.Parse(time.RFC3339Nano, s.AccessExpiresAt)
	if e1 != nil || e2 != nil || !deadline.After(serverNow) || deadline.Sub(serverNow) > time.Hour {
		return errors.New("invalid invitation access deadline")
	}
	g := config.GuestAccess{GrantID: s.GrantID, ReceivedAt: s.ServerNow, AccessExpiresAt: s.AccessExpiresAt, InviterEmail: s.InviterEmail}
	if g.Validate() != nil {
		return errors.New("invalid invitation contact or duration")
	}
	if s.Status == "joining" || s.Status == "paired" {
		if !ValidID(s.AccountID) || !ValidID(s.NetworkID) || !validDigest(s.DeviceID) || !ValidID(s.HostID) || !validHTTPSOrigin(s.ManagementURL) {
			return errors.New("incomplete invitation identity")
		}
		if (s.Credential == "") != (s.HostCredential == "") || s.Credential != "" && (len(s.Credential) < 16 || len(s.HostCredential) < 16 || strings.ContainsAny(s.Credential+s.HostCredential, "\r\n\t ")) {
			return errors.New("incomplete invitation credentials")
		}
	} else if s.Credential != "" || s.HostCredential != "" {
		return errors.New("invitation credentials returned before authorization")
	}
	return nil
}
func containsGuestStatus(status string) bool {
	return status == "provisioning" || status == "authorizing" || status == "joining" || status == "paired"
}

func ValidGuestHostname(grantID, hostname string) bool {
	if hostname != "nexal-guest-"+grantID || len(hostname) > 63 {
		return false
	}
	for _, r := range hostname {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
