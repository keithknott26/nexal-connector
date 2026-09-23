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

func TestEnrollmentSessionValidation(t *testing.T) {
	s := EnrollmentSession{SchemaVersion: 2, SessionID: "123e4567-e89b-12d3-a456-426614174000", UniversalLink: "https://link.nexal.systems/pair/123e4567-e89b-12d3-a456-426614174000#" + strings.Repeat("a", 64), ManualCode: "ABCD-2345", PollToken: strings.Repeat("p", 32), ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano), Status: "waiting"}
	if err := s.Validate("https://coordinator.nexal.systems"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"abcd-2345", "ABCO-2345", "ABCD2345", "ABCD-1234"} {
		s.ManualCode = bad
		if err := s.Validate("https://coordinator.nexal.systems"); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestEnrollmentSessionRejectsMismatchedLinkID(t *testing.T) {
	s := EnrollmentSession{SchemaVersion: 2, SessionID: "123e4567-e89b-12d3-a456-426614174000", UniversalLink: "https://link.nexal.systems/pair/223e4567-e89b-12d3-a456-426614174000#" + strings.Repeat("a", 64), ManualCode: "ABCD-2345", PollToken: strings.Repeat("p", 32), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Status: "waiting"}
	if err := s.Validate("https://coordinator.nexal.systems"); err == nil {
		t.Fatal("accepted link for another session")
	}
}

func TestUniversalLinkStaysOnCoordinator(t *testing.T) {
	s := EnrollmentSession{SchemaVersion: 2, SessionID: "123e4567-e89b-12d3-a456-426614174000", UniversalLink: "https://evil.example/pair/x#" + strings.Repeat("a", 64), ManualCode: "ABCD-2345", PollToken: strings.Repeat("p", 32), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Status: "waiting"}
	if err := s.Validate("https://coordinator.nexal.systems"); err == nil || strings.Contains(err.Error(), "evil") {
		t.Fatal("cross-origin link accepted or leaked")
	}
}

func TestAcknowledgeEnrollmentCredentialsUsesPollBearerAndEmptyBody(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/enrollment-sessions/"+id+"/credentials/ack" || r.Header.Get("Authorization") != "Bearer poll-token" {
			t.Fatalf("unexpected ACK request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"acknowledged":true}`))
	}))
	defer server.Close()
	c, err := New(server.URL, "poll-token", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AcknowledgeEnrollmentCredentials(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentJoiningAllowsCredentialsOnlyBeforeAcknowledgement(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	base := EnrollmentSessionStatus{SchemaVersion: 2, SessionID: id, Status: "joining", Step: "starting_tunnel",
		AccountID: "account_1", NetworkID: "network_1", DeviceID: strings.Repeat("a", 64), HostID: "host_1",
		ManagementURL: "https://control.nexal.systems", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	for _, tc := range []struct {
		name string
		mesh string
		host string
		ok   bool
	}{
		{name: "before ack", mesh: strings.Repeat("m", 16), host: strings.Repeat("h", 16), ok: true},
		{name: "after ack", ok: true},
		{name: "partial secret", mesh: strings.Repeat("m", 16), ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := base
			state.Credential, state.HostCredential = tc.mesh, tc.host
			if valid := validateEnrollmentStatus(state, id); valid != tc.ok {
				t.Fatalf("valid=%v, want %v", valid, tc.ok)
			}
		})
	}
}

func validateEnrollmentStatus(state EnrollmentSessionStatus, id string) bool {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}))
	defer server.Close()
	c, err := New(server.URL, "poll-token", true)
	if err != nil {
		return false
	}
	_, err = c.EnrollmentSessionState(context.Background(), id)
	return err == nil
}
