package peeridentity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"

	"nexal/connector/internal/config"
	"nexal/connector/internal/pool"
)

// Package peeridentity is the "reviewed credential/key recovery policy" that
// internal/pool/identity.go defers to when it says Identity keys are
// "intentionally not automatically persisted or exported". pool generates and
// uses keys; it deliberately takes no position on where they live. Deciding
// that is this package's job. It sits between config and pool rather than
// inside config, because internal/pool's own tests already reach config through
// discovery -> client -> config, so a config->pool import would close a cycle
// that the Go test build rejects.
//
// WHERE THE TWO HALVES LIVE, AND WHY THEY LIVE APART:
//
//   - The PRIVATE key goes to the Secrets store, which is the macOS Keychain in
//     production (NewSecrets refuses anything else off a nonproduction flag).
//     It is handled exactly like the admin token: same store, same lifetime,
//     same blast radius if the machine is compromised. It is never written to
//     the config file, never logged, and never returned by any command.
//   - The FINGERPRINT goes to the config file, because it is a SHA-256 of the
//     public key. Publishing it is the entire point -- the peer on the other
//     Mac must pin it to dial this one -- so treating it as a secret would be
//     cargo-culting rather than security.
//
// This split is why there is no combined "identity file" on disk: the two
// halves have opposite disclosure requirements, and storing them together
// invites code that reads the pair when it only needed the public half.

// peerKeySecret is the Secrets name for this host's peer private key. The
// "peer-" prefix keeps it from ever colliding with the admin token, and means a
// Keychain dump is self-describing about which key is which.
const peerKeySecret = "peer-identity"

// ed25519 seeds are 32 bytes; base64 without padding is 43 characters, safely
// inside validSecret's 16..4096 window and inside its printable-ASCII rule.
//
// The SEED is stored rather than the 64-byte expanded private key. Both
// reconstruct the same identity, but the seed cannot be truncated into a
// still-parseable-but-different key, and ed25519.NewKeyFromSeed is the only
// documented way back, so a malformed stored value fails loudly at parse rather
// than silently yielding a key whose public half no longer matches the
// fingerprint in the config.
const peerSeedEncodedLen = 43

var peerSeedEncoding = base64.RawStdEncoding

// ErrNoIdentity reports that this host has not generated a peer identity yet.
// It is distinct from a corrupt or unreadable one: callers may reasonably
// create an identity on ErrNoIdentity, but must never silently regenerate over
// an existing key, since that would change this host's fingerprint and
// invalidate every peer that has already pinned it.
var ErrNoIdentity = errors.New("no peer identity has been generated for this host")

// LoadIdentity returns this host's persisted peer identity.
//
// A missing key returns ErrNoIdentity so the caller can distinguish "never
// created" from "created and broken". Every other failure -- unparseable seed,
// wrong length, a Keychain that refuses to answer -- is returned as an error
// rather than being papered over with a fresh key, because regenerating would
// silently break peers that already pinned the old fingerprint.
func LoadIdentity(ctx context.Context, secrets config.Secrets) (pool.Identity, error) {
	if secrets == nil {
		return pool.Identity{}, errors.New("a secrets store is required to load the peer identity")
	}
	stored, err := secrets.Get(ctx, peerKeySecret)
	if err != nil || strings.TrimSpace(stored) == "" {
		// Get does not distinguish "absent" from "backend failed" in its error
		// type, and guessing wrong in the unsafe direction would regenerate a
		// key over a healthy one during a transient Keychain problem. So both
		// map to ErrNoIdentity here and the ONLY caller that may act on it,
		// EnsureIdentity, re-checks under the exclusive lock before writing.
		return pool.Identity{}, ErrNoIdentity
	}
	seed, err := peerSeedEncoding.DecodeString(strings.TrimSpace(stored))
	if err != nil || len(seed) != ed25519.SeedSize {
		return pool.Identity{}, errors.New("stored peer identity is malformed; refusing to replace it automatically")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return pool.Identity{}, errors.New("stored peer identity did not yield an ed25519 public key")
	}
	return pool.Identity{PublicKey: pub, PrivateKey: priv}, nil
}

// StoreIdentity persists a peer identity's private half.
//
// It REFUSES to overwrite an existing key. Rotation is a deliberate operation
// with consequences for every peer that pinned the old fingerprint, so it does
// not happen as a side effect of a command that merely wanted to make sure an
// identity existed.
//
// The existence check here is best-effort BY NECESSITY: Secrets.Get cannot
// distinguish "absent" from "backend refused to answer", so a store that is
// failing looks identical to a first run. That gap is closed by the caller, not
// here -- see EnsureIdentity, which requires the config fingerprint as an
// out-of-band witness before it will ever mint a key.
func StoreIdentity(ctx context.Context, secrets config.Secrets, id pool.Identity) error {
	if secrets == nil {
		return errors.New("a secrets store is required to store the peer identity")
	}
	if len(id.PrivateKey) != ed25519.PrivateKeySize || len(id.PublicKey) != ed25519.PublicKeySize {
		return errors.New("refusing to store a malformed peer identity")
	}
	if !id.PublicKey.Equal(id.PrivateKey.Public()) {
		// A mismatched pair would persist a key whose fingerprint never matches
		// what this host publishes, producing dial failures that look like a
		// network fault rather than a storage fault.
		return errors.New("peer identity public and private halves do not correspond")
	}
	if existing, err := secrets.Get(ctx, peerKeySecret); err == nil && strings.TrimSpace(existing) != "" {
		return errors.New("a peer identity already exists; refusing to overwrite it")
	}
	encoded := peerSeedEncoding.EncodeToString(id.PrivateKey.Seed())
	if len(encoded) != peerSeedEncodedLen || !storableSecret(encoded) {
		return errors.New("encoded peer identity is not a storable secret")
	}
	return secrets.Put(ctx, peerKeySecret, encoded)
}

// EnsureIdentity returns this host's peer identity, generating and persisting
// one ONLY when the caller can prove none existed before.
//
// CALL THIS ONLY WITH THE EXCLUSIVE CONFIG LOCK HELD. Generating a key is a
// read-then-write across a store this package does not serialize, so two
// connectors racing here would each mint a key and one would win, leaving the
// loser advertising a fingerprint whose private half is gone.
//
// configuredFingerprint is the value recorded in the connector configuration,
// or "" when none is recorded. It exists because Secrets.Get cannot tell
// "absent" from "the Keychain is refusing to answer", and guessing wrong in the
// unsafe direction silently rotates this host's identity -- breaking every peer
// that pinned the old fingerprint, with no error anywhere to explain it. An
// earlier version of this function did exactly that, and a test caught it.
//
// So the config file is used as an out-of-band WITNESS that an identity already
// exists:
//
//   - fingerprint recorded, key loads, they agree     -> reuse it
//   - fingerprint recorded, key missing or disagrees  -> hard error, never mint
//   - no fingerprint recorded, no key                 -> genuine first run, mint
//
// The consequence is that recovering from a genuinely lost key is a deliberate
// act (clear the fingerprint from the config first), which is the correct
// trade: identity rotation should be something an operator chooses, never
// something a flaky Keychain does on their behalf.
//
// The returned bool reports whether an identity was CREATED, so a command can
// tell the operator that a new fingerprint must be carried to their other
// machine rather than reprinting an existing one as though it were new.
func EnsureIdentity(ctx context.Context, secrets config.Secrets, configuredFingerprint string) (pool.Identity, bool, error) {
	if secrets == nil {
		return pool.Identity{}, false, errors.New("a secrets store is required to load the peer identity")
	}
	id, err := LoadIdentity(ctx, secrets)
	if err == nil {
		if configuredFingerprint != "" && Fingerprint(id) != configuredFingerprint {
			return pool.Identity{}, false, errors.New(
				"the stored peer key does not match the deviceFingerprint in the configuration; " +
					"refusing to publish an identity this host cannot prove")
		}
		return id, false, nil
	}
	if !errors.Is(err, ErrNoIdentity) {
		// Malformed or unreadable, which is never a reason to mint a second key.
		return pool.Identity{}, false, err
	}
	if configuredFingerprint != "" {
		// The witness says an identity existed. The key did not load. Minting
		// here is the exact silent rotation this function exists to prevent.
		return pool.Identity{}, false, errors.New(
			"this host has a deviceFingerprint recorded but its peer key could not be read; " +
				"refusing to generate a replacement, because peers have pinned the recorded fingerprint. " +
				"Restore the key, or clear deviceFingerprint from the configuration to deliberately establish a new identity")
	}
	created, err := pool.NewIdentity()
	if err != nil {
		return pool.Identity{}, false, errors.New("cannot generate a peer identity")
	}
	if err := StoreIdentity(ctx, secrets, created); err != nil {
		return pool.Identity{}, false, err
	}
	return created, true, nil
}

// Fingerprint is this identity's published device ID: pool.DeviceID, which is
// the value a peer pins with `static-peers add --fingerprint`. Defined here so
// no caller re-derives it by hand and risks a differently-cased or
// differently-hashed variant reaching the config, where Validate would reject
// it for reasons that look arbitrary from the outside.
func Fingerprint(id pool.Identity) string {
	return pool.DeviceID(id.PublicKey)
}

// storableSecret mirrors the connector's secret-shape rule: bounded length and
// printable ASCII only. Duplicated deliberately rather than exported from
// config, because widening a validator's visibility to satisfy one caller
// invites drift in the other direction later -- and the encoding here is fixed
// at 43 base64 characters, which this also asserts.
func storableSecret(s string) bool {
	if len(s) < 16 || len(s) > 4096 {
		return false
	}
	for _, r := range s {
		if r <= 32 || r > 126 {
			return false
		}
	}
	return true
}
