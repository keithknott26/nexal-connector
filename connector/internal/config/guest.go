package config

import (
	"errors"
	"net/mail"
	"strings"
	"time"
)

// GuestAccess is a non-secret, durable upper bound on temporary access. It is
// distinct from enrollment-session expiry, which is not a network lease.
type GuestAccess struct {
	MeshHostname    string `json:"meshHostname,omitempty"`
	GrantID         string `json:"grantId"`
	AccessExpiresAt string `json:"accessExpiresAt"`
	ReceivedAt      string `json:"receivedAt"`
	InviterEmail    string `json:"inviterEmail"`
	Expired         bool   `json:"expired"`
}

func (g GuestAccess) Validate() error {
	issued, e1 := time.Parse(time.RFC3339Nano, g.ReceivedAt)
	deadline, e2 := time.Parse(time.RFC3339Nano, g.AccessExpiresAt)
	if g.GrantID == "" || len(g.GrantID) > 80 || strings.ContainsAny(g.GrantID, " /\\\r\n\t") || e1 != nil || e2 != nil || !deadline.After(issued) || deadline.Sub(issued) > time.Hour {
		return errors.New("invalid temporary access deadline")
	}
	addr, err := mail.ParseAddress(g.InviterEmail)
	if err != nil || addr.Address != g.InviterEmail || len(g.InviterEmail) > 254 {
		return errors.New("invalid invitation contact")
	}
	return nil
}
func (g GuestAccess) IsExpired(now time.Time) bool {
	if g.Expired || g.Validate() != nil {
		return true
	}
	issued, _ := time.Parse(time.RFC3339Nano, g.ReceivedAt)
	deadline, _ := time.Parse(time.RFC3339Nano, g.AccessExpiresAt)
	// Reboot with a clock substantially behind receipt must not extend a lease.
	return !now.Before(deadline) || now.Before(issued.Add(-5*time.Second))
}
