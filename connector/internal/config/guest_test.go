package config

import (
	"testing"
	"time"
)

func TestGuestDeadlineFailsClosedAcrossRestartAndClockRollback(t *testing.T) {
	now := time.Now().UTC()
	g := GuestAccess{GrantID: "grant_1", ReceivedAt: now.Format(time.RFC3339Nano), AccessExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), InviterEmail: "owner@example.com"}
	if g.Validate() != nil || g.IsExpired(now.Add(59*time.Minute)) {
		t.Fatal("valid lease rejected")
	}
	if !g.IsExpired(now.Add(time.Hour)) {
		t.Fatal("exact expiry boundary allowed access")
	}
	if !g.IsExpired(now.Add(-time.Minute)) {
		t.Fatal("clock rollback extended access")
	}
	g.AccessExpiresAt = "invalid"
	if !g.IsExpired(now) || g.Validate() == nil {
		t.Fatal("malformed deadline failed open")
	}
	g.AccessExpiresAt = now.Add(2 * time.Hour).Format(time.RFC3339Nano)
	if g.Validate() == nil {
		t.Fatal("unbounded access duration")
	}
	g.AccessExpiresAt = now.Add(time.Hour).Format(time.RFC3339Nano)
	g.InviterEmail = "Name <owner@example.com>"
	if g.Validate() == nil {
		t.Fatal("unvalidated contact display accepted")
	}
}
