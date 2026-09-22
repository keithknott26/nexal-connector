package peerregistry

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nexal/connector/internal/pool"
)

// privateDir is a 0700 directory, which is what AtomicPrivate requires and what
// the connector's real support directory is. t.TempDir() is not guaranteed to
// be 0700, so the fixture matches production rather than relaxing the check.
func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "nexal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	return dir
}

func fixedClock() func() time.Time {
	t := time.Date(2026, 9, 21, 22, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// enrollPeer performs the real invite/prove/enroll handshake so the tests
// persist genuine membership rather than hand-built structs.
func enrollPeer(t *testing.T, r *pool.Registry) (pool.Identity, pool.Member) {
	t.Helper()
	peer, err := pool.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	invitation, err := r.Invite(pool.DeviceID(peer.PublicKey), []pool.Role{pool.Contributor}, time.Minute)
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}
	proof, err := peer.Prove(invitation)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	member, err := r.Enroll(proof)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	return peer, member
}

func TestMissingFileIsAnEmptyRegistryNotAnError(t *testing.T) {
	// A host that has never enrolled a peer must start with an empty registry,
	// not fail to start.
	r, err := Load(filepath.Join(privateDir(t), "peers.json"), fixedClock())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(r.Snapshot()); got != 0 {
		t.Fatalf("Snapshot() = %d members, want 0", got)
	}
}

func TestMembershipSurvivesARestart(t *testing.T) {
	path := filepath.Join(privateDir(t), "peers.json")
	registry := pool.NewRegistry(fixedClock())
	_, member := enrollPeer(t, registry)
	if err := Save(path, registry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	restored, err := Load(path, fixedClock())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := restored.Member(member.DeviceID)
	if !ok {
		t.Fatalf("Member(%s) missing after restart", member.DeviceID)
	}
	if !got.PublicKey.Equal(member.PublicKey) {
		t.Error("restored public key differs from the enrolled one")
	}
	if got.RevokedAt != nil {
		t.Error("restored member is revoked but was not")
	}
}

// Revocation is the whole reason revoked members are persisted. If a revoked
// peer came back as absent, it could enroll again as though it were new.
func TestRevocationSurvivesARestart(t *testing.T) {
	path := filepath.Join(privateDir(t), "peers.json")
	registry := pool.NewRegistry(fixedClock())
	_, member := enrollPeer(t, registry)
	if err := registry.Revoke(member.DeviceID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := Save(path, registry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	restored, err := Load(path, fixedClock())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := restored.Member(member.DeviceID)
	if !ok {
		t.Fatal("revoked member disappeared across a restart, so it could re-enroll as new")
	}
	if got.RevokedAt == nil {
		t.Error("member came back un-revoked")
	}
}

// The attack RestoreRegistry's fingerprint check exists to stop: pair an
// attacker's key with a trusted peer's fingerprint by editing the file, and be
// admitted to the ring under that peer's identity.
func TestSubstitutedKeyUnderATrustedFingerprintIsRefused(t *testing.T) {
	path := filepath.Join(privateDir(t), "peers.json")
	registry := pool.NewRegistry(fixedClock())
	_, trusted := enrollPeer(t, registry)
	if err := Save(path, registry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	attacker, err := pool.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Keep the trusted fingerprint, swap in the attacker's key.
	f.State.Members[0].PublicKey = attacker.PublicKey
	if f.State.Members[0].DeviceID != trusted.DeviceID {
		t.Fatal("test did not preserve the trusted fingerprint")
	}
	tampered, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Load(path, fixedClock()); !errors.Is(err, pool.ErrUnauthorized) {
		t.Fatalf("Load() = %v, want ErrUnauthorized for a key/fingerprint mismatch", err)
	}
}

func TestCorruptAndUnsupportedFilesAreRefusedNotTreatedAsEmpty(t *testing.T) {
	// Each of these must fail loudly. Returning an empty registry instead would
	// silently drop every peer and every revocation.
	for name, content := range map[string]string{
		"not json":          "{{{",
		"unknown version":   `{"version":99,"members":[]}`,
		"missing version":   `{"members":[]}`,
		"unknown field":     `{"version":1,"members":[],"trusted":true}`,
		"truncated members": `{"version":1,"members":[{"deviceId":"abc"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "peers.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := Load(path, fixedClock()); err == nil {
				t.Fatal("Load() = nil error, want a refusal")
			}
		})
	}
}

func TestSavedFileIsPrivateAndStable(t *testing.T) {
	path := filepath.Join(privateDir(t), "peers.json")
	registry := pool.NewRegistry(fixedClock())
	enrollPeer(t, registry)
	enrollPeer(t, registry)
	if err := Save(path, registry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Membership names the machines allowed into the pool; it is not world-readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %v, want 0600", perm)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Snapshot sorts by fingerprint, so re-saving the same membership must not
	// produce a different file and look like a change.
	if err := Save(path, registry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(first) != string(second) {
		t.Error("saving identical membership produced a different file")
	}
}

// A restored registry must be usable as the ring's authority, which is the
// entire point: the collective refuses members it cannot find enrolled.
func TestRestoredRegistryStillAuthorizesTheRing(t *testing.T) {
	path := filepath.Join(privateDir(t), "peers.json")
	registry := pool.NewRegistry(fixedClock())
	peer, member := enrollPeer(t, registry)
	if err := Save(path, registry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	restored, err := Load(path, fixedClock())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := restored.Member(pool.DeviceID(peer.PublicKey))
	if !ok {
		t.Fatal("restored registry does not know the enrolled peer")
	}
	if len(got.Roles) != 1 || got.Roles[0] != pool.Contributor {
		t.Errorf("roles = %v, want [contributor]", got.Roles)
	}
	if !got.EnrolledAt.Equal(member.EnrolledAt) {
		t.Errorf("EnrolledAt = %v, want %v", got.EnrolledAt, member.EnrolledAt)
	}
	_ = ed25519.PublicKey(got.PublicKey)
}

// The bug the two-machine demo caught. `peers invite` and `peers accept` are
// separate processes, so an invitation held only in the inviting process's
// memory could never be completed: accept reloaded the registry, found no such
// invitation and returned "unauthorized". Persisting members alone was not
// enough to make the handshake work across processes.
func TestPendingInvitationSurvivesBetweenProcesses(t *testing.T) {
	path := filepath.Join(privateDir(t), "peers.json")
	inviting := pool.NewRegistry(fixedClock())
	peer, err := pool.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	invitation, err := inviting.Invite(pool.DeviceID(peer.PublicKey), []pool.Role{pool.Contributor}, time.Minute)
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}
	if err := Save(path, inviting); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A different process: reload, then complete the handshake.
	accepting, err := Load(path, fixedClock())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	proof, err := peer.Prove(invitation)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	member, err := accepting.Enroll(proof)
	if err != nil {
		t.Fatalf("Enroll after reload: %v", err)
	}
	if member.DeviceID != pool.DeviceID(peer.PublicKey) {
		t.Errorf("enrolled %s, want %s", member.DeviceID, pool.DeviceID(peer.PublicKey))
	}

	// And it is single-use across processes too: replaying the spent proof in yet
	// another process must fail.
	if err := Save(path, accepting); err != nil {
		t.Fatalf("Save: %v", err)
	}
	replaying, err := Load(path, fixedClock())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := replaying.Enroll(proof); err == nil {
		t.Fatal("a spent proof was accepted a second time")
	}
}

// An invitation the owner never completed must lapse rather than persist forever.
func TestExpiredInvitationIsNotRestored(t *testing.T) {
	path := filepath.Join(privateDir(t), "peers.json")
	base := time.Date(2026, 9, 21, 22, 0, 0, 0, time.UTC)
	clock := base
	registry := pool.NewRegistry(func() time.Time { return clock })
	peer, err := pool.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	invitation, err := registry.Invite(pool.DeviceID(peer.PublicKey), []pool.Role{pool.Contributor}, time.Minute)
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}
	if err := Save(path, registry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	later := base.Add(time.Hour)
	restored, err := Load(path, func() time.Time { return later })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	proof, err := peer.Prove(invitation)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if _, err := restored.Enroll(proof); err == nil {
		t.Fatal("an expired invitation was still accepted after a restart")
	}
}
