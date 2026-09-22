package pool

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

type Role string

const (
	Administrator Role = "administrator"
	ProjectMember Role = "project-member"
	Contributor   Role = "host-contributor"
)

// Identity keys are intentionally not automatically persisted or exported.
// The embedding agent must use its reviewed credential/key recovery policy.
type Identity struct {
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

func NewIdentity() (Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	return Identity{pub, priv}, err
}

func DeviceID(publicKey ed25519.PublicKey) string {
	if len(publicKey) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:])
}

type Invitation struct {
	ID               string    `json:"id"`
	Challenge        string    `json:"challenge"`
	ExpectedDeviceID string    `json:"expectedDeviceId"`
	Roles            []Role    `json:"roles"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

type EnrollmentProof struct {
	InvitationID string            `json:"invitationId"`
	PublicKey    ed25519.PublicKey `json:"publicKey"`
	Signature    []byte            `json:"signature"`
}

type Member struct {
	DeviceID   string            `json:"deviceId"`
	PublicKey  ed25519.PublicKey `json:"publicKey"`
	Roles      []Role            `json:"roles"`
	EnrolledAt time.Time         `json:"enrolledAt"`
	RevokedAt  *time.Time        `json:"revokedAt,omitempty"`
}

// Registry is an in-memory authority. Persist membership/revocation through the
// agent's protected configuration before exposing any network functionality.
// Owner-only Invite pins the fingerprint verified out of band; LAN discovery,
// a hostname, a short unauthenticated PIN or possession of an IP is insufficient.
type Registry struct {
	mu      sync.Mutex
	now     func() time.Time
	invites map[string]Invitation
	members map[string]Member
}

func NewRegistry(clock func() time.Time) *Registry {
	if clock == nil {
		clock = time.Now
	}
	return &Registry{now: clock, invites: make(map[string]Invitation), members: make(map[string]Member)}
}

func validRoles(roles []Role) bool {
	if len(roles) == 0 || len(roles) > 3 {
		return false
	}
	seen := map[Role]bool{}
	for _, role := range roles {
		if (role != Administrator && role != ProjectMember && role != Contributor) || seen[role] {
			return false
		}
		seen[role] = true
	}
	return true
}

func (r *Registry) Invite(expectedDeviceID string, roles []Role, ttl time.Duration) (Invitation, error) {
	if !validDigest(expectedDeviceID) || !validRoles(roles) || ttl <= 0 || ttl > 10*time.Minute {
		return Invitation{}, ErrInvalid
	}
	id, err := randomID(16)
	if err != nil {
		return Invitation{}, err
	}
	challenge, err := randomID(32)
	if err != nil {
		return Invitation{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for key, invitation := range r.invites {
		if !now.Before(invitation.ExpiresAt) {
			delete(r.invites, key)
		}
	}
	if _, exists := r.members[expectedDeviceID]; exists {
		return Invitation{}, ErrConflict // Revoked keys cannot silently re-enroll.
	}
	if len(r.invites) >= 1024 || len(r.members) >= 4096 {
		return Invitation{}, ErrQuota
	}
	v := Invitation{id, challenge, expectedDeviceID, append([]Role(nil), roles...), now.Add(ttl)}
	r.invites[id] = v
	v.Roles = append([]Role(nil), roles...)
	return v, nil
}

func enrollmentMessage(v Invitation) []byte {
	b, _ := json.Marshal(struct {
		Domain string     `json:"domain"`
		Invite Invitation `json:"invite"`
	}{"nexal-private-pool/enroll/v1", v})
	return b
}

func (i Identity) Prove(v Invitation) (EnrollmentProof, error) {
	if !i.valid() || DeviceID(i.PublicKey) != v.ExpectedDeviceID {
		return EnrollmentProof{}, ErrUnauthorized
	}
	return EnrollmentProof{v.ID, append(ed25519.PublicKey(nil), i.PublicKey...),
		ed25519.Sign(i.PrivateKey, enrollmentMessage(v))}, nil
}

func (i Identity) valid() bool {
	if len(i.PrivateKey) != ed25519.PrivateKeySize || len(i.PublicKey) != ed25519.PublicKeySize {
		return false
	}
	return i.PublicKey.Equal(i.PrivateKey.Public())
}

func cloneMember(m Member) Member {
	m.PublicKey = append(ed25519.PublicKey(nil), m.PublicKey...)
	m.Roles = append([]Role(nil), m.Roles...)
	if m.RevokedAt != nil {
		t := *m.RevokedAt
		m.RevokedAt = &t
	}
	return m
}

func (r *Registry) Enroll(p EnrollmentProof) (Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.invites[p.InvitationID]
	if !ok {
		return Member{}, ErrUnauthorized
	}
	if !r.now().Before(v.ExpiresAt) {
		delete(r.invites, p.InvitationID)
		return Member{}, ErrExpired
	}
	id := DeviceID(p.PublicKey)
	if id == "" || id != v.ExpectedDeviceID ||
		!ed25519.Verify(p.PublicKey, enrollmentMessage(v), p.Signature) {
		return Member{}, ErrUnauthorized
	}
	if _, exists := r.members[id]; exists {
		return Member{}, ErrConflict
	}
	m := Member{id, append(ed25519.PublicKey(nil), p.PublicKey...),
		append([]Role(nil), v.Roles...), r.now(), nil}
	delete(r.invites, p.InvitationID)
	r.members[id] = m
	return cloneMember(m), nil
}

func (r *Registry) Member(id string) (Member, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[id]
	return cloneMember(m), ok
}

func (r *Registry) Revoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[id]
	if !ok {
		return ErrUnauthorized
	}
	if m.RevokedAt == nil {
		now := r.now()
		m.RevokedAt = &now
		r.members[id] = m
	}
	return nil
}

func hasRole(m Member, role Role) bool {
	for _, v := range m.Roles {
		if v == role {
			return true
		}
	}
	return false
}

// ReplicaReceipt is a signed trusted-host assertion about a verified, synced
// object. It is neither proof of independent hardware nor remote attestation.
type ReplicaReceipt struct {
	DeviceID   string       `json:"deviceId"`
	Digest     string       `json:"digest"`
	Size       int64        `json:"size"`
	Class      StorageClass `json:"class"`
	VerifiedAt time.Time    `json:"verifiedAt"`
	Nonce      string       `json:"nonce"`
	Signature  []byte       `json:"signature"`
}

func receiptMessage(v ReplicaReceipt) []byte {
	v.Signature = nil
	b, _ := json.Marshal(struct {
		Domain  string         `json:"domain"`
		Receipt ReplicaReceipt `json:"receipt"`
	}{"nexal-private-pool/replica/v1", v})
	return b
}

func (r *Registry) VerifyReceipt(v ReplicaReceipt, now time.Time, maxAge time.Duration) error {
	if !validDigest(v.Digest) || v.Size < 0 || !v.Class.valid() || len(v.Nonce) != 64 ||
		!validDigest(v.Nonce) || maxAge <= 0 || v.VerifiedAt.IsZero() || v.VerifiedAt.After(now) ||
		now.Sub(v.VerifiedAt) >= maxAge {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[v.DeviceID]
	if !ok || m.RevokedAt != nil || !hasRole(m, Contributor) ||
		v.VerifiedAt.Before(m.EnrolledAt) || !ed25519.Verify(m.PublicKey, receiptMessage(v), v.Signature) {
		return ErrUnauthorized
	}
	return nil
}

// Snapshot returns every member, revoked ones included, for persistence by the
// embedding agent. Revoked members are deliberately included: dropping them
// would make a revocation indistinguishable from a machine that was never
// enrolled, and a peer that came back would enroll cleanly as a new member.
// Revocation has to outlive the process to mean anything.
func (r *Registry) Snapshot() []Member {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Member, 0, len(r.members))
	for _, m := range r.members {
		out = append(out, cloneMember(m))
	}
	// Fingerprint order, so a persisted file is byte-stable across runs and a
	// diff shows a real membership change rather than map iteration order.
	slices.SortFunc(out, func(a, b Member) int { return strings.Compare(a.DeviceID, b.DeviceID) })
	return out
}

// RestoreRegistry rebuilds a registry from persisted members.
//
// Enrollment cannot be replayed: an invitation is one-use and its proof signs a
// random challenge that no longer exists, so a restored registry has to be
// reconstructed from the resulting members rather than re-derived. That makes
// this function the trust boundary for the on-disk file, and every member is
// therefore re-validated here rather than assumed.
//
// The DeviceID is recomputed from the public key and must match the stored one.
// That check is the important one: without it, an attacker who could edit the
// configuration could pair their own key with a trusted peer's fingerprint and
// be admitted to the ring under that peer's identity. Pending invitations are
// intentionally NOT restored -- they are short-lived by design, and a process
// restart is exactly when an outstanding invitation should lapse.
// RegistrySnapshot is everything needed to rebuild a registry.
//
// Invitations are included because the connector's CLI is inherently
// multi-process: `peers invite` and `peers accept` are separate invocations, so
// an invitation that lived only in the inviting process's memory could never be
// accepted by the process that runs accept. They remain bounded by their own
// ExpiresAt, and RestoreRegistry drops expired ones rather than restoring them.
type RegistrySnapshot struct {
	Members     []Member     `json:"members"`
	Invitations []Invitation `json:"invitations,omitempty"`
}

// SnapshotState returns members and still-valid invitations for persistence.
func (r *Registry) SnapshotState() RegistrySnapshot {
	state := RegistrySnapshot{Members: r.Snapshot()}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, v := range r.invites {
		// An already-expired invitation is not worth writing; it would be
		// dropped on the next load anyway.
		if now.Before(v.ExpiresAt) {
			state.Invitations = append(state.Invitations, v)
		}
	}
	slices.SortFunc(state.Invitations, func(a, b Invitation) int { return strings.Compare(a.ID, b.ID) })
	return state
}

func RestoreRegistry(clock func() time.Time, state RegistrySnapshot) (*Registry, error) {
	r := NewRegistry(clock)
	for _, v := range state.Invitations {
		// Expiry is normal, not corruption: an invitation the owner never
		// completed simply lapses.
		if !r.now().Before(v.ExpiresAt) {
			continue
		}
		if v.ID == "" || v.Challenge == "" || !validDigest(v.ExpectedDeviceID) || !validRoles(v.Roles) {
			return nil, fmt.Errorf("%w: persisted invitation is malformed", ErrInvalid)
		}
		if _, exists := r.invites[v.ID]; exists {
			return nil, fmt.Errorf("%w: invitation %s appears twice", ErrConflict, v.ID)
		}
		r.invites[v.ID] = v
	}
	for _, m := range state.Members {
		id := DeviceID(m.PublicKey)
		if id == "" || id != m.DeviceID {
			return nil, fmt.Errorf("%w: member fingerprint does not match its public key", ErrUnauthorized)
		}
		if !validRoles(m.Roles) {
			return nil, fmt.Errorf("%w: member %s has invalid roles", ErrInvalid, m.DeviceID)
		}
		if m.EnrolledAt.IsZero() {
			return nil, fmt.Errorf("%w: member %s has no enrollment time", ErrInvalid, m.DeviceID)
		}
		if m.RevokedAt != nil && m.RevokedAt.Before(m.EnrolledAt) {
			return nil, fmt.Errorf("%w: member %s was revoked before it was enrolled", ErrInvalid, m.DeviceID)
		}
		if _, exists := r.members[id]; exists {
			return nil, fmt.Errorf("%w: member %s appears twice", ErrConflict, m.DeviceID)
		}
		r.members[id] = cloneMember(m)
	}
	return r, nil
}
