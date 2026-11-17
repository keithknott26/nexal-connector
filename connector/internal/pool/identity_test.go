package pool

import (
	"crypto/ed25519"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func enrolledIdentity(t *testing.T, registry *Registry) Identity {
	t.Helper()
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	invite, err := registry.Invite(DeviceID(identity.PublicKey), []Role{Contributor}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := identity.Prove(invite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Enroll(proof); err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestEnrollmentProofPinReplayExpiryAndRevocation(t *testing.T) {
	now := time.Now()
	r := NewRegistry(func() time.Time { return now })
	identity, _ := NewIdentity()
	attacker, _ := NewIdentity()
	v, err := r.Invite(DeviceID(identity.PublicKey), []Role{Contributor}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Possession of the entire invitation is insufficient without the pinned key.
	wrong := EnrollmentProof{v.ID, attacker.PublicKey, ed25519.Sign(attacker.PrivateKey, enrollmentMessage(v))}
	if _, err := r.Enroll(wrong); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unverified LAN device accepted")
	}
	if _, err := attacker.Prove(v); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("wrong fingerprint accepted")
	}
	proof, _ := identity.Prove(v)
	tampered := v
	tampered.Roles = []Role{Administrator}
	proof.Signature = ed25519.Sign(identity.PrivateKey, enrollmentMessage(tampered))
	if _, err := r.Enroll(proof); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("role alteration accepted")
	}
	proof, _ = identity.Prove(v)
	member, err := r.Enroll(proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Enroll(proof); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("replayed invitation accepted")
	}
	// Returned slices are detached from the authority.
	member.PublicKey[0] ^= 1
	member.Roles[0] = Administrator
	stored, _ := r.Member(DeviceID(identity.PublicKey))
	if !stored.PublicKey.Equal(identity.PublicKey) || !hasRole(stored, Contributor) {
		t.Fatal("caller mutated authority")
	}
	if err := r.Revoke(DeviceID(identity.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Invite(DeviceID(identity.PublicKey), []Role{Contributor}, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatal("revoked key silently re-enrolled")
	}
	expiredIdentity, _ := NewIdentity()
	v, _ = r.Invite(DeviceID(expiredIdentity.PublicKey), []Role{Contributor}, time.Minute)
	proof, _ = expiredIdentity.Prove(v)
	now = now.Add(time.Minute)
	if _, err := r.Enroll(proof); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired invitation accepted: %v", err)
	}
}

func TestConcurrentInvitationConsumedOnce(t *testing.T) {
	r := NewRegistry(nil)
	identity, _ := NewIdentity()
	v, _ := r.Invite(DeviceID(identity.PublicKey), []Role{Contributor}, time.Minute)
	proof, _ := identity.Prove(v)
	var successes atomic.Int64
	var wg sync.WaitGroup
	for idx := 0; idx < 50; idx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Enroll(proof); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("invitation consumed %d times", successes.Load())
	}
}

func TestRestoreRegistryRejectsCorruptState(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := DeviceID(pub)
	enrolled := now.Add(-time.Hour)
	revokedEarly := enrolled.Add(-time.Minute)
	goodMember := Member{DeviceID: id, PublicKey: pub, Roles: []Role{Contributor}, EnrolledAt: enrolled}
	goodInvite := Invitation{ID: "inv", Challenge: "c", ExpectedDeviceID: id, Roles: []Role{Contributor}, ExpiresAt: now.Add(time.Hour)}

	with := func(m func(*Member)) Member { v := goodMember; m(&v); return v }
	cases := []struct {
		name  string
		state RegistrySnapshot
		want  error
	}{
		{"invite without id", RegistrySnapshot{Invitations: []Invitation{{Challenge: "c", ExpectedDeviceID: id, Roles: []Role{Contributor}, ExpiresAt: goodInvite.ExpiresAt}}}, ErrInvalid},
		{"duplicate invite", RegistrySnapshot{Invitations: []Invitation{goodInvite, goodInvite}}, ErrConflict},
		{"member fingerprint mismatch", RegistrySnapshot{Members: []Member{with(func(m *Member) { m.DeviceID = "bogus" })}}, ErrUnauthorized},
		{"member bad roles", RegistrySnapshot{Members: []Member{with(func(m *Member) { m.Roles = nil })}}, ErrInvalid},
		{"member no enrollment time", RegistrySnapshot{Members: []Member{with(func(m *Member) { m.EnrolledAt = time.Time{} })}}, ErrInvalid},
		{"revoked before enrolled", RegistrySnapshot{Members: []Member{with(func(m *Member) { m.RevokedAt = &revokedEarly })}}, ErrInvalid},
		{"duplicate member", RegistrySnapshot{Members: []Member{goodMember, goodMember}}, ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RestoreRegistry(clock, tc.state); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	expired := goodInvite
	expired.ID, expired.Challenge = "", ""
	expired.ExpiresAt = now.Add(-time.Second)
	if _, err := RestoreRegistry(clock, RegistrySnapshot{Invitations: []Invitation{expired}}); err != nil {
		t.Fatalf("expired malformed invitation should be skipped, got %v", err)
	}
}

func TestInviteRejectsBadInputAndEnforcesQuotaAndPrunesExpired(t *testing.T) {
	now := time.Now()
	r := NewRegistry(func() time.Time { return now })
	id, _ := NewIdentity()
	dev := DeviceID(id.PublicKey)
	for name, call := range map[string]func() error{
		"bad device": func() error { _, err := r.Invite("nope", []Role{Contributor}, time.Minute); return err },
		"bad roles":  func() error { _, err := r.Invite(dev, []Role{"root"}, time.Minute); return err },
		"zero ttl":   func() error { _, err := r.Invite(dev, []Role{Contributor}, 0); return err },
		"long ttl":   func() error { _, err := r.Invite(dev, []Role{Contributor}, 11*time.Minute); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}

	// Fill the invitation table; the next invite must hit the quota.
	for i := 0; i < 1024; i++ {
		other, _ := NewIdentity()
		if _, err := r.Invite(DeviceID(other.PublicKey), []Role{Contributor}, time.Minute); err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
	}
	if _, err := r.Invite(dev, []Role{Contributor}, time.Minute); !errors.Is(err, ErrQuota) {
		t.Fatalf("over-quota invite err = %v, want ErrQuota", err)
	}

	// Once the old invitations expire they are pruned, freeing the quota.
	now = now.Add(2 * time.Minute)
	if _, err := r.Invite(dev, []Role{Contributor}, time.Minute); err != nil {
		t.Fatalf("invite after expiry: %v", err)
	}
	if got := len(r.SnapshotState().Invitations); got != 1 {
		t.Errorf("pending invitations = %d, want 1 after pruning", got)
	}
}
