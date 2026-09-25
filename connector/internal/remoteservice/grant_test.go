package remoteservice

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"
)

func signed(t *testing.T, now time.Time) (Grant, ed25519.PublicKey) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	g := Grant{ID: "g1", SourceHostID: "source", TargetHostID: "target", Service: SSH,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Nonce: "random-unique-value"}
	text, err := g.SigningText()
	if err != nil {
		t.Fatal(err)
	}
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(text)))
	return g, pub
}

func TestVerifyBindsSignatureHostsServiceAndTime(t *testing.T) {
	now := time.Now().UTC()
	g, pub := signed(t, now)
	if err := Verify(g, pub, "source", "target", now); err != nil {
		t.Fatal(err)
	}
	checks := []Grant{g, g, g, g}
	checks[0].TargetHostID = "other"
	checks[1].Service = VNC
	checks[2].ExpiresAt = now
	checks[3].ExpiresAt = g.IssuedAt.Add(25 * time.Hour)
	for i, bad := range checks {
		if Verify(bad, pub, "source", "target", now) == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestCapabilitiesNeverTreatAuthorizationAsEnabled(t *testing.T) {
	now := time.Now().UTC()
	g, _ := signed(t, now)
	caps := Capabilities([]string{"vnc", "smb", "unknown"}, []Grant{g}, now)
	if caps[0].Service != SSH || caps[0].ObservedEnabled || !caps[0].TemporarilyAuthorized || !caps[0].RequiresAdminApproval {
		t.Fatalf("ssh = %+v", caps[0])
	}
	if !caps[1].ObservedEnabled || !caps[2].ObservedEnabled {
		t.Fatalf("observations lost: %+v", caps)
	}
	if caps[3].RequiresAdminApproval {
		t.Fatal("wake incorrectly requires privileged service enablement")
	}
}

func TestSigningTextRejectsDelimiterInjection(t *testing.T) {
	g := Grant{ID: "g|1", SourceHostID: "s", TargetHostID: "t", Service: SSH, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Nonce: "n"}
	if _, err := g.SigningText(); err == nil {
		t.Fatal("accepted ambiguous canonical text")
	}
}
