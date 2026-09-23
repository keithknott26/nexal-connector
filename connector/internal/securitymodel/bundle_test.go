package securitymodel

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type bundleSource struct{ bundle EncryptedBundle }

func (s bundleSource) Fetch(context.Context, string) (EncryptedBundle, error) { return s.bundle, nil }

type digestVerifier struct{}

func (digestVerifier) Verify(_, _ string, message, signature []byte) error {
	want := sha256.Sum256(message)
	if string(want[:]) != string(signature) {
		return errors.New("bad signature")
	}
	return nil
}

type keyFixture struct{ key []byte }

func (k *keyFixture) BundleKey(context.Context, string, string) ([]byte, error) { return k.key, nil }

func sealedFixture(t *testing.T, device string, now time.Time, revisions ...uint64) (EncryptedBundle, []byte) {
	t.Helper()
	plain := []byte("proprietary model and rules")
	digest := sha256.Sum256(plain)
	revision := uint64(7)
	if len(revisions) == 1 {
		revision = revisions[0]
	}
	m := BundleManifest{SchemaVersion: 1, BundleID: "bundle-1", DeviceID: device, ModelVersion: "model-7", RulesVersion: "rules-9", Revision: revision,
		PlaintextSize: len(plain), SHA256: hex.EncodeToString(digest[:]), Cipher: "AES-256-GCM", Signature: "test-signature-v1", KeyID: "runtime-key-1",
		IssuedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	ciphertext := aead.Seal(nil, nonce, plain, manifest)
	sig := sha256.Sum256(manifest)
	return EncryptedBundle{ManifestJSON: manifest, Signature: sig[:], Nonce: nonce, Ciphertext: ciphertext}, key
}

type persistentFloor struct{ values map[string]uint64 }

func (f *persistentFloor) Load(_ context.Context, device string) (uint64, error) {
	return f.values[device], nil
}
func (f *persistentFloor) Advance(_ context.Context, device string, revision uint64) error {
	if revision < f.values[device] {
		return errors.New("rollback")
	}
	f.values[device] = revision
	return nil
}

type activator struct {
	calls int
	fail  bool
}

func (a *activator) Activate(context.Context, BundleManifest, []byte) error {
	a.calls++
	if a.fail {
		return errors.New("activation failed")
	}
	return nil
}

func TestRollbackFloorPersistsAcrossLoaderRestart(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	floor := &persistentFloor{values: make(map[string]uint64)}
	bundle7, key7 := sealedFixture(t, "device-1", now, 7)
	first := &activator{}
	if _, err := RetrieveAndActivate(context.Background(), bundleSource{bundle7}, digestVerifier{}, &keyFixture{key: key7}, floor, first, "device-1", now); err != nil {
		t.Fatal(err)
	}
	if floor.values["device-1"] != 7 || first.calls != 1 {
		t.Fatalf("activation did not persist floor: floor=%d calls=%d", floor.values["device-1"], first.calls)
	}

	// A new source/key/activator represents a restarted loader. Only the
	// caller-owned persistent floor is shared across instances.
	bundle6, key6 := sealedFixture(t, "device-1", now, 6)
	second := &activator{}
	if _, err := RetrieveAndActivate(context.Background(), bundleSource{bundle6}, digestVerifier{}, &keyFixture{key: key6}, floor, second, "device-1", now); err == nil || second.calls != 0 {
		t.Fatal("restarted loader accepted a bundle below the persistent floor")
	}

	bundle8, key8 := sealedFixture(t, "device-1", now, 8)
	failing := &activator{fail: true}
	if _, err := RetrieveAndActivate(context.Background(), bundleSource{bundle8}, digestVerifier{}, &keyFixture{key: key8}, floor, failing, "device-1", now); err == nil {
		t.Fatal("failed activation reported success")
	}
	if floor.values["device-1"] != 7 {
		t.Fatal("rollback floor advanced before successful activation")
	}
}

func TestRetrieveVerifiesDecryptsAndZeroesRuntimeKey(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	bundle, key := sealedFixture(t, "device-1", now)
	keys := &keyFixture{key: key}
	plain, manifest, err := Retrieve(context.Background(), bundleSource{bundle}, digestVerifier{}, keys, "device-1", now)
	if err != nil || string(plain) != "proprietary model and rules" || manifest.BundleID != "bundle-1" {
		t.Fatalf("retrieve: manifest=%+v plaintext=%q err=%v", manifest, plain, err)
	}
	for _, b := range key {
		if b != 0 {
			t.Fatal("runtime bundle key was retained after decryption")
		}
	}
}

func TestRetrieveRejectsTamperingAndWrongDevice(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*EncryptedBundle){
		"signature":  func(b *EncryptedBundle) { b.Signature[0] ^= 1 },
		"ciphertext": func(b *EncryptedBundle) { b.Ciphertext[len(b.Ciphertext)-1] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bundle, key := sealedFixture(t, "device-1", now)
			mutate(&bundle)
			if _, _, err := Retrieve(context.Background(), bundleSource{bundle}, digestVerifier{}, &keyFixture{key: key}, "device-1", now); err == nil {
				t.Fatal("tampered bundle accepted")
			}
		})
	}
	bundle, key := sealedFixture(t, "device-1", now)
	if _, _, err := Retrieve(context.Background(), bundleSource{bundle}, digestVerifier{}, &keyFixture{key: key}, "device-2", now); err == nil {
		t.Fatal("bundle for a different device accepted")
	}
}
