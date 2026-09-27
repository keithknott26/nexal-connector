package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func guestReply() EnrollmentSessionStatus {
	now := time.Now().UTC()
	return EnrollmentSessionStatus{SchemaVersion: 2, SessionID: "123e4567-e89b-12d3-a456-426614174000", Status: "joining", GrantID: "123e4567-e89b-12d3-a456-426614174001", MeshHostname: "nexal-guest-123e4567-e89b-12d3-a456-426614174001", ServerNow: now.Format(time.RFC3339Nano), AccessExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), InviterEmail: "owner@example.com", AccountID: "account_1", NetworkID: "network_1", DeviceID: strings.Repeat("a", 64), HostID: "host_1", ManagementURL: "https://mesh.example.com", Credential: strings.Repeat("m", 32), HostCredential: strings.Repeat("h", 32)}
}
func TestGuestRedeemBindsCodeAndSessionWithoutHostBearer(t *testing.T) {
	response := guestReply()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v2/network-invitations/redeem" || r.Header.Get("Authorization") != "" {
			t.Error("wrong credential boundary")
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["code"] != "ABCD2345" || in["sessionId"] != response.SessionID || in["pollToken"] != strings.Repeat("p", 32) {
			t.Error("wrong invitation binding")
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	c, err := New(server.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.RedeemGuestInvitation(context.Background(), "abcd-2345", response.SessionID, strings.Repeat("p", 32))
	if err != nil || out.GrantID != response.GrantID {
		t.Fatal(err)
	}
}
func TestGuestGrantRejectsMissingLeaseContactOrIdentity(t *testing.T) {
	for _, mutate := range []func(*EnrollmentSessionStatus){
		func(s *EnrollmentSessionStatus) { s.AccessExpiresAt = "" }, func(s *EnrollmentSessionStatus) { s.InviterEmail = "javascript:evil" }, func(s *EnrollmentSessionStatus) { s.ServerNow = "" }, func(s *EnrollmentSessionStatus) { s.DeviceID = "wrong" }, func(s *EnrollmentSessionStatus) { s.Credential = "" }, func(s *EnrollmentSessionStatus) { s.Status = "waiting" }, func(s *EnrollmentSessionStatus) { s.ManagementURL = "http://example.com" },
	} {
		s := guestReply()
		mutate(&s)
		if s.ValidateGuest(s.SessionID) == nil {
			t.Fatal("unsafe grant accepted")
		}
	}
	for _, code := range []string{"ABCD1234", "ABCD2345\nextra", "$(whoami)", "ABCD23456"} {
		if _, err := NormalizeGuestCode(code); err == nil {
			t.Fatal("invalid code accepted")
		}
	}
}
