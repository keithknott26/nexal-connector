package peeridentity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"

	"nexal/connector/internal/pool"
)

// memSecrets is a Secrets implementation that records writes, so a test can
// assert not merely the returned value but whether the key was persisted at all
// and how many times.
type memSecrets struct {
	mu      sync.Mutex
	values  map[string]string
	puts    int
	getErr  error
	putErr  error
	failGet bool
}

func newMem() *memSecrets { return &memSecrets{values: map[string]string{}} }

func (m *memSecrets) Get(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet {
		return "", errors.New("keychain unavailable")
	}
	if m.getErr != nil {
		return "", m.getErr
	}
	v, ok := m.values[name]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func (m *memSecrets) Put(_ context.Context, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	m.puts++
	m.values[name] = value
	return nil
}

func TestEnsureIdentityCreatesThenReuses(t *testing.T) {
	m := newMem()
	ctx := context.Background()

	first, created, err := EnsureIdentity(ctx, m, "")
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	fp := Fingerprint(first)
	if len(fp) != 64 {
		t.Fatalf("fingerprint is not 64 hex characters: %q", fp)
	}
	if strings.ToLower(fp) != fp {
		t.Fatalf("fingerprint must be lowercase to satisfy config validation: %q", fp)
	}

	second, created, err := EnsureIdentity(ctx, m, "")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	// The load-bearing assertion. If this ever reports created=true, every peer
	// that pinned the first fingerprint is silently broken.
	if created {
		t.Fatal("second call reported created=true; the identity must be stable across calls")
	}
	if Fingerprint(second) != fp {
		t.Fatalf("fingerprint changed across calls: %s -> %s", fp, Fingerprint(second))
	}
	if m.puts != 1 {
		t.Fatalf("expected exactly one write to the secrets store, got %d", m.puts)
	}
}

func TestStoredValueIsTheSeedAndNotThePrivateKey(t *testing.T) {
	m := newMem()
	id, _, err := EnsureIdentity(context.Background(), m, "")
	if err != nil {
		t.Fatal(err)
	}
	stored := m.values[peerKeySecret]
	if len(stored) != peerSeedEncodedLen {
		t.Fatalf("stored secret should be a %d-character encoded seed, got %d", peerSeedEncodedLen, len(stored))
	}
	raw, err := peerSeedEncoding.DecodeString(stored)
	if err != nil || len(raw) != ed25519.SeedSize {
		t.Fatalf("stored secret does not decode to a 32-byte seed: %v", err)
	}
	// The full 64-byte private key must NOT be what is stored: a truncated copy
	// of it would still parse as a key, whereas a truncated seed cannot.
	if strings.Contains(stored, base64.RawStdEncoding.EncodeToString(id.PrivateKey)) {
		t.Fatal("the expanded private key was stored instead of the seed")
	}
	// Round-tripping the seed must reproduce the same public half.
	if !ed25519.NewKeyFromSeed(raw).Public().(ed25519.PublicKey).Equal(id.PublicKey) {
		t.Fatal("stored seed does not reconstruct the original identity")
	}
}

func TestLoadIdentityReportsMissingDistinctlyFromCorrupt(t *testing.T) {
	ctx := context.Background()

	if _, err := LoadIdentity(ctx, newMem()); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("absent key should report ErrNoIdentity, got %v", err)
	}

	for _, bad := range []string{
		"not-base64!!",
		base64.RawStdEncoding.EncodeToString(make([]byte, 16)), // wrong length
		base64.RawStdEncoding.EncodeToString(make([]byte, 64)), // expanded key, not a seed
		"   ",
	} {
		m := newMem()
		m.values[peerKeySecret] = bad
		_, err := LoadIdentity(ctx, m)
		if err == nil {
			t.Fatalf("corrupt value %q was accepted", bad)
		}
		// Corrupt must NOT look like absent, or EnsureIdentity would generate a
		// replacement key over a possibly-recoverable one.
		if errors.Is(err, ErrNoIdentity) && strings.TrimSpace(bad) != "" {
			t.Fatalf("corrupt value %q reported as ErrNoIdentity; it would be silently replaced", bad)
		}
	}
}

func TestEnsureIdentityRefusesToReplaceACorruptKey(t *testing.T) {
	m := newMem()
	m.values[peerKeySecret] = "wildly-invalid"
	_, created, err := EnsureIdentity(context.Background(), m, "")
	if err == nil {
		t.Fatal("expected an error rather than a silent regeneration")
	}
	if created {
		t.Fatal("a corrupt stored key must never be overwritten automatically")
	}
	if m.puts != 0 {
		t.Fatalf("nothing should have been written, got %d writes", m.puts)
	}
	if m.values[peerKeySecret] != "wildly-invalid" {
		t.Fatal("the existing stored value was modified")
	}
}

func TestStoreIdentityRefusesOverwrite(t *testing.T) {
	m := newMem()
	ctx := context.Background()
	first, _, err := EnsureIdentity(ctx, m, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := StoreIdentity(ctx, m, other); err == nil {
		t.Fatal("StoreIdentity overwrote an existing peer identity")
	}
	reloaded, err := LoadIdentity(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(reloaded) != Fingerprint(first) {
		t.Fatal("the original identity did not survive the refused overwrite")
	}
}

func TestStoreIdentityRejectsMalformedAndMismatchedPairs(t *testing.T) {
	ctx := context.Background()

	if err := StoreIdentity(ctx, newMem(), pool.Identity{}); err == nil {
		t.Fatal("empty identity was accepted")
	}

	a, _ := pool.NewIdentity()
	b, _ := pool.NewIdentity()
	// A public half that does not correspond to the private half would make this
	// host publish a fingerprint it cannot prove.
	mismatched := pool.Identity{PublicKey: b.PublicKey, PrivateKey: a.PrivateKey}
	m := newMem()
	if err := StoreIdentity(ctx, m, mismatched); err == nil {
		t.Fatal("mismatched key pair was accepted")
	}
	if m.puts != 0 {
		t.Fatal("a rejected identity was still written")
	}
}

func TestLoadIdentityRequiresASecretsStore(t *testing.T) {
	if _, err := LoadIdentity(context.Background(), nil); err == nil {
		t.Fatal("a nil secrets store was accepted")
	}
	if _, _, err := EnsureIdentity(context.Background(), nil, ""); err == nil {
		t.Fatal("EnsureIdentity accepted a nil secrets store")
	}
}

// A Keychain that is present but refusing to answer must not cause a new key to
// be minted over the real one. LoadIdentity cannot distinguish the two cases
// from Secrets.Get's error, so it reports ErrNoIdentity -- which means the
// write attempt still has to fail closed at StoreIdentity, because the existing
// value is discoverable there.
func TestTransientSecretsFailureDoesNotSilentlyRotate(t *testing.T) {
	m := newMem()
	ctx := context.Background()
	original, _, err := EnsureIdentity(ctx, m, "")
	if err != nil {
		t.Fatal(err)
	}

	// The witness: the fingerprint this host already published. With it set, a
	// failing store must refuse rather than mint a replacement.
	witness := Fingerprint(original)

	m.failGet = true
	if _, _, err := EnsureIdentity(ctx, m, witness); err == nil {
		t.Fatal("a failing secrets store should not yield a usable identity")
	}
	if m.puts != 1 {
		t.Fatalf("a second key was minted during a transient failure: %d writes", m.puts)
	}

	// Once the store recovers, the ORIGINAL identity must still be the one
	// returned -- proving the failed call neither replaced nor corrupted it.
	m.failGet = false
	recovered, created, err := EnsureIdentity(ctx, m, witness)
	if err != nil {
		t.Fatalf("identity did not survive a transient failure: %v", err)
	}
	if created {
		t.Fatal("a new identity was reported after the store recovered")
	}
	if Fingerprint(recovered) != Fingerprint(original) {
		t.Fatalf("fingerprint changed across a transient failure: %s -> %s",
			Fingerprint(original), Fingerprint(recovered))
	}
}

func TestFingerprintMatchesPoolDeviceID(t *testing.T) {
	id, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	// Fingerprint must be exactly pool.DeviceID and nothing else, or the value
	// written into the config would not match what a peer pins.
	if Fingerprint(id) != pool.DeviceID(id.PublicKey) {
		t.Fatal("Fingerprint diverged from pool.DeviceID")
	}
	if Fingerprint(pool.Identity{}) != "" {
		t.Fatal("an empty identity must not yield a fingerprint")
	}
}

// The bug this guards against was live in an earlier version of EnsureIdentity
// and was caught by TestTransientSecretsFailureDoesNotSilentlyRotate: because
// Secrets.Get cannot distinguish "absent" from "backend broken", a failing
// Keychain looked exactly like a first run, and StoreIdentity's existence check
// failed OPEN and wrote a new key over the healthy one. The config fingerprint
// is the out-of-band witness that closes that gap.
func TestRecordedFingerprintPreventsMintingAReplacement(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	// A host that published a fingerprint but whose key is now unreadable.
	m.values[peerKeySecret] = ""
	witness := strings.Repeat("a", 64)

	_, created, err := EnsureIdentity(ctx, m, witness)
	if err == nil {
		t.Fatal("expected a refusal: a fingerprint is recorded but no key is readable")
	}
	if created {
		t.Fatal("a replacement identity was minted despite a recorded fingerprint")
	}
	if m.puts != 0 {
		t.Fatalf("expected no writes, got %d", m.puts)
	}
	if !strings.Contains(err.Error(), "clear deviceFingerprint") {
		t.Fatalf("the error should tell the operator how to deliberately recover, got: %v", err)
	}
}

// A stored key that disagrees with the recorded fingerprint means this host
// would publish an identity it cannot prove -- typically a config restored from
// another machine. It must be an error, not a silent correction in either
// direction.
func TestStoredKeyDisagreeingWithRecordedFingerprintIsAnError(t *testing.T) {
	ctx := context.Background()
	m := newMem()
	id, _, err := EnsureIdentity(ctx, m, "")
	if err != nil {
		t.Fatal(err)
	}
	someoneElse := strings.Repeat("b", 64)
	if _, _, err := EnsureIdentity(ctx, m, someoneElse); err == nil {
		t.Fatal("a stored key disagreeing with the recorded fingerprint was accepted")
	}
	// The real key must be untouched and still usable once the config is fixed.
	again, created, err := EnsureIdentity(ctx, m, Fingerprint(id))
	if err != nil || created {
		t.Fatalf("the original identity was damaged: created=%v err=%v", created, err)
	}
	if Fingerprint(again) != Fingerprint(id) {
		t.Fatal("fingerprint changed")
	}
}

// First run with nothing recorded anywhere is the ONLY case that may mint.
func TestGenuineFirstRunMints(t *testing.T) {
	m := newMem()
	id, created, err := EnsureIdentity(context.Background(), m, "")
	if err != nil || !created {
		t.Fatalf("a genuine first run must mint: created=%v err=%v", created, err)
	}
	if len(Fingerprint(id)) != 64 || m.puts != 1 {
		t.Fatalf("unexpected result: fp=%q puts=%d", Fingerprint(id), m.puts)
	}
}
