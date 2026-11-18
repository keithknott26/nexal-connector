package pool

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"testing"
)

// ringSenderFingerprint is what turns a verified TLS peer into a ring sender
// identity, so anything but exactly one Ed25519 certificate must be refused.
func TestRingSenderFingerprint(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	good := &x509.Certificate{PublicKey: pub}

	got, ok := ringSenderFingerprint([]*x509.Certificate{good})
	if !ok || got != DeviceID(pub) {
		t.Fatalf("single ed25519 cert = (%q, %v), want (%q, true)", got, ok, DeviceID(pub))
	}

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]*x509.Certificate{
		"nil":             nil,
		"empty":           {},
		"two certs":       {good, good},
		"non-ed25519 key": {{PublicKey: &ec.PublicKey}},
	}
	for name, certs := range cases {
		if fp, ok := ringSenderFingerprint(certs); ok || fp != "" {
			t.Errorf("%s: = (%q, %v), want (\"\", false)", name, fp, ok)
		}
	}
}
