package p2p

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"nexal/connector/internal/pool"
)

// TestIdentityRoundTrips is the test the plan's identity claim rests on:
// "libp2p peer IDs derive from public keys, which maps directly onto DeviceID =
// sha256(ed25519 pubkey)". If that mapping is not total and deterministic, the
// authorization gate is comparing two different things and §30.2 is unenforceable
// over libp2p.
func TestIdentityRoundTrips(t *testing.T) {
	for i := 0; i < 64; i++ {
		id, err := pool.NewIdentity()
		if err != nil {
			t.Fatalf("identity: %v", err)
		}
		want := pool.DeviceID(id.PublicKey)
		if want == "" {
			t.Fatal("pool.DeviceID returned empty for a fresh identity")
		}
		pid, err := PeerIDFor(id.PublicKey)
		if err != nil {
			t.Fatalf("PeerIDFor: %v", err)
		}
		got, err := DeviceIDFromPeerID(pid)
		if err != nil {
			t.Fatalf("DeviceIDFromPeerID: %v", err)
		}
		if got != want {
			t.Fatalf("round trip mismatch: peer ID derived %q, pool.DeviceID says %q", got, want)
		}
		// And through the private-key path the host actually uses, so the host's
		// peer ID is the same peer ID.
		priv, err := PrivateKey(id)
		if err != nil {
			t.Fatalf("PrivateKey: %v", err)
		}
		fromPriv, err := peer.IDFromPublicKey(priv.GetPublic())
		if err != nil {
			t.Fatalf("IDFromPublicKey: %v", err)
		}
		if fromPriv != pid {
			t.Fatalf("host key produces peer ID %q but public key maps to %q", fromPriv, pid)
		}
		// Decoding the textual form must not change the answer: the coordinator
		// will carry peer IDs as strings.
		decoded, err := peer.Decode(pid.String())
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		again, err := DeviceIDFromPeerID(decoded)
		if err != nil || again != want {
			t.Fatalf("string round trip: %q %v", again, err)
		}
	}
}

// TestPrivateKeyRejectsMismatchedPair guards against one machine ending up with
// two identities: a pool.Identity whose public half is not its private half's
// public key would give a peer ID whose DeviceID is not the connector's DeviceID.
func TestPrivateKeyRejectsMismatchedPair(t *testing.T) {
	a, _ := pool.NewIdentity()
	b, _ := pool.NewIdentity()
	if _, err := PrivateKey(pool.Identity{PublicKey: a.PublicKey, PrivateKey: b.PrivateKey}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched keypair accepted: %v", err)
	}
	if _, err := PrivateKey(pool.Identity{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero identity accepted")
	}
	short := make(ed25519.PublicKey, 31)
	if _, err := PeerIDFor(short); !errors.Is(err, ErrInvalid) {
		t.Fatal("short public key accepted")
	}
}

// TestNonEd25519PeerIDRefused: an RSA/ECDSA peer ID carries no recoverable key,
// so no DeviceID exists for it. The honest answer is an error, because the
// alternative is a gate that falls back on something weaker than a fingerprint —
// and peer.go:28 requires an exact fingerprint allowlist, not a best effort.
func TestNonEd25519PeerIDRefused(t *testing.T) {
	// ECDSA: peer ID is a sha256 multihash, key not embedded.
	priv, _, err := libcrypto.GenerateECDSAKeyPair(rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa: %v", err)
	}
	pid, err := peer.IDFromPublicKey(priv.GetPublic())
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	if _, err := DeviceIDFromPeerID(pid); err == nil {
		t.Fatal("an ECDSA peer ID produced a DeviceID; the mapping must refuse it")
	}
	// Secp256k1 IS embedded in the peer ID, so this case specifically tests the
	// key-TYPE check rather than the embedding check.
	spriv, _, err := libcrypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("secp: %v", err)
	}
	spid, err := peer.IDFromPublicKey(spriv.GetPublic())
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	if _, err := DeviceIDFromPeerID(spid); !errors.Is(err, ErrKeyType) {
		t.Fatalf("secp256k1 peer ID must be refused with ErrKeyType, got %v", err)
	}
	if _, err := DeviceIDFromPeerID(peer.ID("")); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty peer ID accepted")
	}
}
