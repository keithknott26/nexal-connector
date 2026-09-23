package securitymodel

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const MaxBundleBytes = 64 << 20

type BundleManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	BundleID      string `json:"bundleId"`
	DeviceID      string `json:"deviceId"`
	ModelVersion  string `json:"modelVersion"`
	RulesVersion  string `json:"rulesVersion"`
	Revision      uint64 `json:"revision"`
	PlaintextSize int    `json:"plaintextSize"`
	SHA256        string `json:"sha256"`
	Cipher        string `json:"cipher"`
	Signature     string `json:"signatureAlgorithm"`
	KeyID         string `json:"keyId"`
	IssuedAt      string `json:"issuedAt"`
	ExpiresAt     string `json:"expiresAt"`
}

// EncryptedBundle contains no decryption key. ManifestJSON is signed exactly as
// received, avoiding ambiguous re-marshalling during verification.
type EncryptedBundle struct {
	ManifestJSON []byte `json:"manifest"`
	Signature    []byte `json:"signature"`
	Nonce        []byte `json:"nonce"`
	Ciphertext   []byte `json:"ciphertext"`
}

type BundleSource interface {
	Fetch(context.Context, string) (EncryptedBundle, error)
}

type SignatureVerifier interface {
	Verify(keyID, algorithm string, message, signature []byte) error
}

type KeyProvider interface {
	BundleKey(context.Context, string, string) ([]byte, error)
}

// RollbackFloor is persistent caller-owned state (Keychain/TPM-backed storage
// in production). This package deliberately supplies no local file fallback.
type RollbackFloor interface {
	Load(context.Context, string) (uint64, error)
	Advance(context.Context, string, uint64) error
}

type BundleActivator interface {
	Activate(context.Context, BundleManifest, []byte) error
}

// RetrieveAndActivate rejects revisions below the persisted floor. The floor is
// advanced only after the verified plaintext has been successfully activated.
func RetrieveAndActivate(ctx context.Context, source BundleSource, verifier SignatureVerifier, keys KeyProvider, floor RollbackFloor, activator BundleActivator, deviceID string, now time.Time) (BundleManifest, error) {
	if floor == nil || activator == nil {
		return BundleManifest{}, errors.New("bundle activation is not configured")
	}
	plain, manifest, err := Retrieve(ctx, source, verifier, keys, deviceID, now)
	if err != nil {
		return BundleManifest{}, err
	}
	defer zero(plain)
	minimum, err := floor.Load(ctx, deviceID)
	if err != nil {
		return BundleManifest{}, errors.New("security bundle rollback floor unavailable")
	}
	if manifest.Revision < minimum {
		return BundleManifest{}, errors.New("security bundle rollback rejected")
	}
	if err := activator.Activate(ctx, manifest, plain); err != nil {
		return BundleManifest{}, errors.New("security bundle activation failed")
	}
	if manifest.Revision > minimum {
		if err := floor.Advance(ctx, deviceID, manifest.Revision); err != nil {
			return BundleManifest{}, errors.New("security bundle rollback floor persistence failed")
		}
	}
	return manifest, nil
}

// Retrieve verifies identity, freshness and signature before requesting the
// runtime-only device key, then authenticates/decrypts and hashes the payload.
func Retrieve(ctx context.Context, source BundleSource, verifier SignatureVerifier, keys KeyProvider, deviceID string, now time.Time) ([]byte, BundleManifest, error) {
	if source == nil || verifier == nil || keys == nil || !validID(deviceID, 128) {
		return nil, BundleManifest{}, errors.New("bundle retrieval is not configured")
	}
	envelope, err := source.Fetch(ctx, deviceID)
	if err != nil {
		return nil, BundleManifest{}, errors.New("security bundle unavailable")
	}
	if len(envelope.ManifestJSON) == 0 || len(envelope.ManifestJSON) > 16<<10 || len(envelope.Ciphertext) == 0 || len(envelope.Ciphertext) > MaxBundleBytes+32 {
		return nil, BundleManifest{}, errors.New("invalid security bundle envelope")
	}
	var manifest BundleManifest
	d := json.NewDecoder(&sliceReader{b: envelope.ManifestJSON})
	d.DisallowUnknownFields()
	if d.Decode(&manifest) != nil || d.Decode(new(any)) != io.EOF || validateManifest(manifest, deviceID, now) != nil {
		return nil, BundleManifest{}, errors.New("invalid security bundle manifest")
	}
	if err := verifier.Verify(manifest.KeyID, manifest.Signature, envelope.ManifestJSON, envelope.Signature); err != nil {
		return nil, BundleManifest{}, errors.New("security bundle signature rejected")
	}
	key, err := keys.BundleKey(ctx, deviceID, manifest.BundleID)
	if err != nil {
		return nil, BundleManifest{}, errors.New("security bundle key unavailable")
	}
	defer zero(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, BundleManifest{}, errors.New("invalid security bundle key")
	}
	aead, _ := cipher.NewGCM(block)
	if len(envelope.Nonce) != aead.NonceSize() {
		return nil, BundleManifest{}, errors.New("invalid security bundle nonce")
	}
	plain, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, envelope.ManifestJSON)
	if err != nil || len(plain) != manifest.PlaintextSize {
		return nil, BundleManifest{}, errors.New("security bundle authentication failed")
	}
	digest := sha256.Sum256(plain)
	if hex.EncodeToString(digest[:]) != manifest.SHA256 {
		zero(plain)
		return nil, BundleManifest{}, errors.New("security bundle digest mismatch")
	}
	return plain, manifest, nil
}

func validateManifest(m BundleManifest, deviceID string, now time.Time) error {
	issued, e1 := time.Parse(time.RFC3339, m.IssuedAt)
	expires, e2 := time.Parse(time.RFC3339, m.ExpiresAt)
	_, e3 := hex.DecodeString(m.SHA256)
	if m.SchemaVersion != 1 || m.Revision == 0 || m.DeviceID != deviceID || !validID(m.BundleID, 128) || !validID(m.ModelVersion, 64) || !validID(m.RulesVersion, 64) ||
		m.PlaintextSize < 1 || m.PlaintextSize > MaxBundleBytes || len(m.SHA256) != 64 || e3 != nil || m.Cipher != "AES-256-GCM" ||
		!validID(m.Signature, 64) || !validID(m.KeyID, 128) || e1 != nil || e2 != nil || issued.After(now.Add(time.Minute)) || !expires.After(now) || expires.Sub(issued) > 7*24*time.Hour {
		return errors.New("invalid manifest")
	}
	return nil
}

func validID(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

type sliceReader struct{ b []byte }

func (r *sliceReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
