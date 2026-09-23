package client

import (
	"strings"
	"testing"
	"time"
)

func TestEnrollmentSessionValidation(t *testing.T) {
	s := EnrollmentSession{SchemaVersion: 2, SessionID: "123e4567-e89b-12d3-a456-426614174000", UniversalLink: "https://link.nexal.systems/pair/opaque#" + strings.Repeat("a", 64), ManualCode: "ABCD-2345", PollToken: strings.Repeat("p", 32), ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano), Status: "waiting"}
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

func TestUniversalLinkStaysOnCoordinator(t *testing.T) {
	s := EnrollmentSession{SchemaVersion: 2, SessionID: "123e4567-e89b-12d3-a456-426614174000", UniversalLink: "https://evil.example/pair/x#" + strings.Repeat("a", 64), ManualCode: "ABCD-2345", PollToken: strings.Repeat("p", 32), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Status: "waiting"}
	if err := s.Validate("https://coordinator.nexal.systems"); err == nil || strings.Contains(err.Error(), "evil") {
		t.Fatal("cross-origin link accepted or leaked")
	}
}
