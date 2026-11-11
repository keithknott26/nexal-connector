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

func TestValidIDCharsetAndLength(t *testing.T) {
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	cases := []struct {
		name string
		in   string
		max  int
		want bool
	}{
		{"empty", "", 64, false},
		{"allowed charset", "Aa09_-.", 64, true},
		{"exactly max", string(long[:64]), 64, true},
		{"over max", string(long), 64, false},
		{"space", "a b", 64, false},
		{"slash", "a/b", 64, false},
		{"non-ascii", "café", 64, false},
		{"newline", "a\n", 64, false},
	}
	for _, c := range cases {
		if got := validID(c.in, c.max); got != c.want {
			t.Errorf("%s: validID(%q,%d)=%v want %v", c.name, c.in, c.max, got, c.want)
		}
	}
}

type errSource struct{}

func (errSource) Fetch(context.Context, string) (EncryptedBundle, error) {
	return EncryptedBundle{}, errors.New("network down")
}

type errKeys struct{}

func (errKeys) BundleKey(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("no key")
}

type errFloor struct {
	loadErr, advanceErr error
}

func (f errFloor) Load(context.Context, string) (uint64, error)  { return 0, f.loadErr }
func (f errFloor) Advance(context.Context, string, uint64) error { return f.advanceErr }

func TestRetrieveAndActivateErrorBranches(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	bundle, key := sealedFixture(t, "device-1", now)
	src := bundleSource{bundle}
	keys := func() *keyFixture { return &keyFixture{key: append([]byte(nil), key...)} }

	// Unconfigured collaborators.
	if _, err := RetrieveAndActivate(ctx, src, digestVerifier{}, keys(), nil, &activator{}, "device-1", now); err == nil {
		t.Fatal("nil floor accepted")
	}
	if _, err := RetrieveAndActivate(ctx, src, digestVerifier{}, keys(), &persistentFloor{values: map[string]uint64{}}, nil, "device-1", now); err == nil {
		t.Fatal("nil activator accepted")
	}
	if _, _, err := Retrieve(ctx, nil, digestVerifier{}, keys(), "device-1", now); err == nil {
		t.Fatal("nil source accepted")
	}
	if _, _, err := Retrieve(ctx, src, digestVerifier{}, keys(), "bad id!", now); err == nil {
		t.Fatal("invalid device id accepted")
	}

	// Retrieve failures propagate through RetrieveAndActivate and never activate.
	act := &activator{}
	if _, err := RetrieveAndActivate(ctx, errSource{}, digestVerifier{}, keys(), &persistentFloor{values: map[string]uint64{}}, act, "device-1", now); err == nil || act.calls != 0 {
		t.Fatalf("fetch error: err=%v calls=%d", err, act.calls)
	}
	if _, _, err := Retrieve(ctx, src, digestVerifier{}, errKeys{}, "device-1", now); err == nil {
		t.Fatal("key provider error accepted")
	}

	// Envelope validation.
	empty := bundle
	empty.Ciphertext = nil
	if _, _, err := Retrieve(ctx, bundleSource{empty}, digestVerifier{}, keys(), "device-1", now); err == nil {
		t.Fatal("empty ciphertext accepted")
	}
	badNonce := bundle
	badNonce.Nonce = []byte{1, 2, 3}
	if _, _, err := Retrieve(ctx, bundleSource{badNonce}, digestVerifier{}, keys(), "device-1", now); err == nil {
		t.Fatal("short nonce accepted")
	}
	if _, _, err := Retrieve(ctx, src, digestVerifier{}, &keyFixture{key: []byte("short")}, "device-1", now); err == nil {
		t.Fatal("invalid AES key length accepted")
	}

	// Floor failures.
	act = &activator{}
	if _, err := RetrieveAndActivate(ctx, src, digestVerifier{}, keys(), errFloor{loadErr: errors.New("keychain locked")}, act, "device-1", now); err == nil || act.calls != 0 {
		t.Fatalf("floor load error: err=%v calls=%d", err, act.calls)
	}
	if _, err := RetrieveAndActivate(ctx, src, digestVerifier{}, keys(), errFloor{advanceErr: errors.New("write failed")}, &activator{}, "device-1", now); err == nil {
		t.Fatal("floor persistence failure reported success")
	}

	// Equal revision: activates but does not rewrite the floor.
	floor := &persistentFloor{values: map[string]uint64{"device-1": 7}}
	if _, err := RetrieveAndActivate(ctx, src, digestVerifier{}, keys(), floor, &activator{}, "device-1", now); err != nil {
		t.Fatalf("same-revision reactivation: %v", err)
	}
}
