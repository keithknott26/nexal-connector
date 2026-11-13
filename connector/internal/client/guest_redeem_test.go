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

func TestRedeemGuestInvitationRejectsBadInputBeforeAnyRequest(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	c, err := New(server.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	session := guestReply().SessionID
	good := strings.Repeat("p", 32)
	cases := map[string][3]string{
		"bad code":         {"nope", session, good},
		"bad session":      {"ABCD2345", "not-a-uuid", good},
		"short token":      {"ABCD2345", session, strings.Repeat("p", 31)},
		"oversize token":   {"ABCD2345", session, strings.Repeat("p", 4097)},
		"whitespace token": {"ABCD2345", session, strings.Repeat("p", 15) + " " + strings.Repeat("p", 16)},
	}
	for name, in := range cases {
		if _, err := c.RedeemGuestInvitation(context.Background(), in[0], in[1], in[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if hits != 0 {
		t.Fatalf("invalid input reached the network %d times", hits)
	}
}

func TestRedeemGuestInvitationDiscardsGrantForOtherSession(t *testing.T) {
	reply := guestReply()
	reply.SessionID = "123e4567-e89b-12d3-a456-426614174099"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(reply) }))
	defer server.Close()
	c, err := New(server.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.RedeemGuestInvitation(context.Background(), "ABCD2345", guestReply().SessionID, strings.Repeat("p", 32))
	if err == nil || out.GrantID != "" || out.Credential != "" {
		t.Fatalf("grant for a different session leaked: err=%v out=%+v", err, out)
	}
}

func TestRedeemGuestInvitationSurfacesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	defer server.Close()
	c, err := New(server.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RedeemGuestInvitation(context.Background(), "ABCD2345", guestReply().SessionID, strings.Repeat("p", 32)); err == nil {
		t.Fatal("server refusal not reported")
	}
}

func TestValidateGuestDeadlineAndCredentialBoundaries(t *testing.T) {
	now := time.Now().UTC()
	for name, mutate := range map[string]func(*EnrollmentSessionStatus){
		"deadline beyond one hour": func(s *EnrollmentSessionStatus) {
			s.AccessExpiresAt = now.Add(time.Hour + time.Minute).Format(time.RFC3339Nano)
		},
		"deadline not after now": func(s *EnrollmentSessionStatus) { s.AccessExpiresAt = s.ServerNow },
		"short mesh credential":  func(s *EnrollmentSessionStatus) { s.Credential = "short" },
		"short host credential":  func(s *EnrollmentSessionStatus) { s.HostCredential = "short" },
		"credential whitespace": func(s *EnrollmentSessionStatus) {
			s.Credential = strings.Repeat("m", 15) + " " + strings.Repeat("m", 16)
		},
		"only host credential": func(s *EnrollmentSessionStatus) { s.Credential = "" },
		"mismatched hostname":  func(s *EnrollmentSessionStatus) { s.MeshHostname = "nexal-guest-other" },
		"wrong session":        func(s *EnrollmentSessionStatus) { s.SessionID = "123e4567-e89b-12d3-a456-426614174098" },
	} {
		s := guestReply()
		mutate(&s)
		if s.ValidateGuest(guestReply().SessionID) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Credentials must not be handed out before the grant is authorized.
	s := guestReply()
	s.Status = "authorizing"
	if s.ValidateGuest(s.SessionID) == nil {
		t.Error("credentials accepted on a pre-join status")
	}
	s.Credential, s.HostCredential = "", ""
	if err := s.ValidateGuest(s.SessionID); err != nil {
		t.Errorf("credential-free authorizing grant rejected: %v", err)
	}
}

func TestValidGuestHostnameRejectsUppercaseAndOverlongNames(t *testing.T) {
	id := "123e4567-e89b-12d3-a456-426614174001"
	if !ValidGuestHostname(id, "nexal-guest-"+id) {
		t.Fatal("canonical hostname rejected")
	}
	upper := strings.ToUpper(id)
	if ValidGuestHostname(upper, "nexal-guest-"+upper) {
		t.Error("uppercase hostname accepted")
	}
	long := strings.Repeat("a", 60)
	if ValidGuestHostname(long, "nexal-guest-"+long) {
		t.Error("hostname over 63 bytes accepted")
	}
}
