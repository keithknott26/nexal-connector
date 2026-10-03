package client

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// Sharing requests: the one v2 host route the connector calls to answer a
// coordinator sharing.request delivered over the presence stream.
//
//	POST /api/v2/hosts/sharing-requests/{requestId}
//	     {"state":"enabled"|"declined"|"failed"|"already_on","detail":"<=200 chars"}
//
// It authenticates with the host bearer token, exactly like heartbeat and the
// wake-info PUT, and it is newer than every other route here, so an older
// coordinator answers 404 (or 503 while the feature flag is off). IsNotSupported
// names that case so callers can stay quiet about it instead of logging a fault.
//
// The report is a statement about what THIS Mac did, not a request for
// permission: the owner's per-service opt-in lives on the coordinator, which is
// what sets autoApproved on the inbound frame.

// The four states the coordinator accepts. They are the wire values; do not
// rename them.
const (
	SharingEnabled   = "enabled"
	SharingDeclined  = "declined"
	SharingFailed    = "failed"
	SharingAlreadyOn = "already_on"
)

// MaxSharingDetail is the coordinator's cap on the optional detail string.
const MaxSharingDetail = 200

// ValidSharingState reports whether state is one of the four wire values.
func ValidSharingState(state string) bool {
	switch state {
	case SharingEnabled, SharingDeclined, SharingFailed, SharingAlreadyOn:
		return true
	}
	return false
}

// CleanSharingDetail reduces detail to something safe to send and to log: no
// control characters (so nothing can forge a log line) and at most
// MaxSharingDetail bytes, cut on a rune boundary.
func CleanSharingDetail(detail string) string {
	text := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, detail)
	text = strings.TrimSpace(text)
	if len(text) > MaxSharingDetail {
		text = strings.ToValidUTF8(text[:MaxSharingDetail], "")
	}
	return text
}

// ReportSharingResult tells the coordinator what this Mac did about one sharing
// request. The response body is not interpreted, so repeating a report is
// harmless; the coordinator settles the request by its own id.
func (c *Client) ReportSharingResult(ctx context.Context, requestID, state, detail string) error {
	if !ValidID(requestID) {
		return errors.New("invalid sharing request id")
	}
	if !ValidSharingState(state) {
		return errors.New("invalid sharing request state")
	}
	body := struct {
		State  string `json:"state"`
		Detail string `json:"detail,omitempty"`
	}{State: state, Detail: CleanSharingDetail(detail)}
	return c.callLenient(ctx, "POST", "/api/v2/hosts/sharing-requests/"+url.PathEscape(requestID), body, nil)
}
