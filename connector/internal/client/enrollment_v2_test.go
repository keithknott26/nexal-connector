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

func validEnrollmentRequest() EnrollmentSessionRequest {
	return EnrollmentSessionRequest{SchemaVersion: EnrollmentSchemaVersion, DeviceName: "Studio", DeviceID: strings.Repeat("a", 64),
		PublicKey: "pk", Platform: "darwin", Architecture: "arm64"}
}

func TestCreateEnrollmentSessionRejectsInvalidRequestWithoutNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid request reached the coordinator")
	}))
	defer server.Close()
	c, err := New(server.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*EnrollmentSessionRequest){
		"schema":     func(r *EnrollmentSessionRequest) { r.SchemaVersion = 1 },
		"blank name": func(r *EnrollmentSessionRequest) { r.DeviceName = "  " },
		"long name":  func(r *EnrollmentSessionRequest) { r.DeviceName = strings.Repeat("x", 81) },
		"bad device": func(r *EnrollmentSessionRequest) { r.DeviceID = "ABC" },
		"no key":     func(r *EnrollmentSessionRequest) { r.PublicKey = "" },
		"platform":   func(r *EnrollmentSessionRequest) { r.Platform = "linux" },
		"no arch":    func(r *EnrollmentSessionRequest) { r.Architecture = "" },
	} {
		req := validEnrollmentRequest()
		mutate(&req)
		if _, err := c.CreateEnrollmentSession(context.Background(), req); err == nil {
			t.Errorf("%s: accepted invalid request", name)
		}
	}
}

func TestCreateEnrollmentSessionValidatesCoordinatorResponse(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	for _, tc := range []struct {
		name   string
		status string
		ok     bool
	}{
		{name: "waiting", status: "waiting", ok: true},
		{name: "already paired", status: "paired", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v2/enrollment-sessions" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(EnrollmentSession{SchemaVersion: 2, SessionID: id,
					UniversalLink: "https://link.nexal.systems/pair/" + id + "#" + strings.Repeat("a", 64),
					ManualCode:    "ABCD-2345", PollToken: strings.Repeat("p", 32), Status: tc.status,
					ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)})
			}))
			defer server.Close()
			c, err := New(server.URL, "", true)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.CreateEnrollmentSession(context.Background(), validEnrollmentRequest())
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v, want ok=%v", err, tc.ok)
			}
			if tc.ok && got.SessionID != id {
				t.Fatalf("session id = %q", got.SessionID)
			}
			if !tc.ok && got.SessionID != "" {
				t.Fatal("rejected session was still returned")
			}
		})
	}
}

func TestEnrollmentSessionStateRejectsMalformedStatus(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	good := EnrollmentSessionStatus{SchemaVersion: 2, SessionID: id, Status: "claimed", Step: "claimed",
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	if !validateEnrollmentStatus(good, id) {
		t.Fatal("baseline status rejected")
	}
	for name, mutate := range map[string]func(*EnrollmentSessionStatus){
		"other session":   func(s *EnrollmentSessionStatus) { s.SessionID = "223e4567-e89b-12d3-a456-426614174000" },
		"schema":          func(s *EnrollmentSessionStatus) { s.SchemaVersion = 1 },
		"unknown status":  func(s *EnrollmentSessionStatus) { s.Status = "bogus" },
		"empty step":      func(s *EnrollmentSessionStatus) { s.Step = "" },
		"newline in step": func(s *EnrollmentSessionStatus) { s.Step = "a\nb" },
		"bad expiry":      func(s *EnrollmentSessionStatus) { s.ExpiresAt = "soon" },
		"waiting expired": func(s *EnrollmentSessionStatus) {
			s.Status = "waiting"
			s.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		},
		"identity too soon": func(s *EnrollmentSessionStatus) { s.HostID = "host_1" },
	} {
		s := good
		mutate(&s)
		if validateEnrollmentStatus(s, id) {
			t.Errorf("%s: accepted", name)
		}
	}
	c, err := New("http://127.0.0.1:1", "t", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnrollmentSessionState(context.Background(), "not-a-uuid"); err == nil {
		t.Error("accepted malformed session id")
	}
}
