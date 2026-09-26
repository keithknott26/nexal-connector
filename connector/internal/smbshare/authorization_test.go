package smbshare

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"
)

func authorizedFixture(t *testing.T, now time.Time) (RecoveryAuthorization, RecoverySelection, ed25519.PublicKey) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := RecoveryAuthorization{GrantID: "grant-1", HelperHostID: "helper-1", NetworkID: "network-1", AccountID: "account-1",
		UserID: "user-1", Username: "owner@example.test", ShareName: "NexalRecovery", MountPath: "/Volumes/NexalRecovery/user-1",
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Nonce: "unique-random-nonce"}
	text, err := a.signingText()
	if err != nil {
		t.Fatal(err)
	}
	a.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(text)))
	s := RecoverySelection{HelperHostID: a.HelperHostID, NetworkID: a.NetworkID, AccountID: a.AccountID,
		Username: a.Username, ShareName: a.ShareName, MountPath: a.MountPath}
	return a, s, pub
}

func TestRecoveryAuthorizationBindsSelectedHelperUserAndMount(t *testing.T) {
	now := time.Now().UTC()
	grant, selected, pub := authorizedFixture(t, now)
	if err := VerifyRecoveryAuthorization(grant, pub, selected, now, true); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*RecoverySelection){
		func(s *RecoverySelection) { s.HelperHostID = "other" },
		func(s *RecoverySelection) { s.AccountID = "other" },
		func(s *RecoverySelection) { s.Username = "other@example.test" },
		func(s *RecoverySelection) { s.MountPath = "/Volumes/AnotherUsersBackup" },
	}
	for i, mutate := range mutations {
		candidate := selected
		mutate(&candidate)
		if VerifyRecoveryAuthorization(grant, pub, candidate, now, true) == nil {
			t.Fatalf("binding mutation %d accepted", i)
		}
	}
}

func TestRecoveryAuthorizationRequiresAdminApprovalAndLiveSignature(t *testing.T) {
	now := time.Now().UTC()
	grant, selected, pub := authorizedFixture(t, now)
	if VerifyRecoveryAuthorization(grant, pub, selected, now, false) == nil {
		t.Fatal("coordinator grant replaced administrator approval")
	}
	tampered := grant
	tampered.ShareName = "OtherShare"
	changed := selected
	changed.ShareName = "OtherShare"
	if VerifyRecoveryAuthorization(tampered, pub, changed, now, true) == nil {
		t.Fatal("tampered signed grant accepted")
	}
	if VerifyRecoveryAuthorization(grant, pub, selected, grant.ExpiresAt, true) == nil {
		t.Fatal("expired grant accepted")
	}
}

func TestRecoveryAuthorityPublicKeyDecodeIsStrict(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := base64.RawURLEncoding.EncodeToString(pub)
	if got, err := DecodeRecoveryPublicKey(raw); err != nil || !got.Equal(pub) {
		t.Fatal("valid key rejected")
	}
	for _, bad := range []string{"", "not-base64", base64.RawURLEncoding.EncodeToString(pub[:10])} {
		if _, err := DecodeRecoveryPublicKey(bad); err == nil {
			t.Fatalf("bad key %q accepted", bad)
		}
	}
}
