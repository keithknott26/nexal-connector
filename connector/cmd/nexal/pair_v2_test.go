package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

type ackFixture struct{ err error }

func (a ackFixture) AcknowledgeEnrollmentCredentials(context.Context, string) error { return a.err }

func TestAcknowledgementNeverReinterpretsFailureAsSuccess(t *testing.T) {
	for _, err := range []error{errors.New("network unavailable"), &client.StatusError{Status: http.StatusUnauthorized}, &client.StatusError{Status: http.StatusConflict}, &client.StatusError{Status: http.StatusInternalServerError}} {
		if acknowledgePersistedCredentials(context.Background(), ackFixture{err: err}, "session") == nil {
			t.Fatalf("unconfirmed ACK treated as success: %v", err)
		}
	}
}

func TestCrashRecoveryRequiresMatchingDurableSession(t *testing.T) {
	const session = "123e4567-e89b-12d3-a456-426614174000"
	cfg := config.Config{HostID: "host_1", Enrollment: &config.EnrollmentState{SchemaVersion: 2, SessionID: session, Status: "paired"}}
	if !persistedEnrollment(cfg, session) {
		t.Fatal("durable paired marker not recognized")
	}
	cfg.Enrollment.SessionID = "223e4567-e89b-12d3-a456-426614174000"
	if persistedEnrollment(cfg, session) {
		t.Fatal("another session authorized ACK recovery")
	}
	cfg.Enrollment.SessionID = session
	cfg.HostID = ""
	if persistedEnrollment(cfg, session) {
		t.Fatal("incomplete local persistence authorized ACK recovery")
	}
}

func TestRecoveredStatusContainsNoCredentialMaterial(t *testing.T) {
	cfg := config.Config{HostID: "host_1", Enrollment: &config.EnrollmentState{SchemaVersion: 2, SessionID: "s", Status: "paired", AccountID: "a", NetworkID: "n", DeviceID: "d", PairedAt: "p", ExpiresAt: "e"}}
	got := enrollmentStatusFromConfig(cfg, "s")
	if got.Credential != "" || got.HostCredential != "" || got.Status != "paired" || got.ExpiresAt != "e" {
		t.Fatalf("unsafe recovered status: %#v", got)
	}
}

func TestV2EnrollmentSupersedesStaleCloudflaredConfiguration(t *testing.T) {
	legacy := &config.Tunnel{TokenFile: "/obsolete/token"}
	if !shouldRunLegacyTunnel(config.Config{Tunnel: legacy}) {
		t.Fatal("a legacy-only configuration unexpectedly lost its tunnel")
	}
	if shouldRunLegacyTunnel(config.Config{Tunnel: legacy, Enrollment: &config.EnrollmentState{SchemaVersion: 2, Status: "joining"}}) {
		t.Fatal("stale cloudflared configuration can still abort a v2 mesh connector")
	}
}
