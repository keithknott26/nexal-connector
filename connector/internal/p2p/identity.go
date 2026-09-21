package p2p

import (
	"crypto/ed25519"
	"errors"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	cryptopb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/libp2p/go-libp2p/core/peer"

	"nexal/connector/internal/pool"
)

// ONE IDENTITY, TWO ENCODINGS — and that is the whole reason libp2p fits here.
//
// The repo's device fingerprint is pool.DeviceID = sha256(ed25519 pubkey)
// (internal/pool/identity.go:33-39). A libp2p peer ID for an Ed25519 key is the
// IDENTITY multihash of the marshalled public key — the key is carried inside
// the peer ID rather than hashed into it. So the mapping is total and
// deterministic in both directions with no directory lookup:
//
//	ed25519 pubkey ──PeerIDFor──▶ peer.ID ──DeviceIDFromPeerID──▶ DeviceID
//	ed25519 pubkey ──pool.DeviceID────────────────────────────────▶ DeviceID
//
// and the two right-hand results are byte-identical. TestIdentityRoundTrips
// asserts that, because a mapping that is merely "believed" deterministic is how
// an authorization check ends up comparing two different things.
//
// WHY THE ROUND TRIP MATTERS TO SECURITY, not just to tidiness. The gater in
// authz.go authorizes a connection by deriving a DeviceID from the peer ID
// libp2p authenticated, then checking it against the coordinator's allowlist. If
// the derivation were lossy, or if it silently accepted a key type whose peer ID
// does NOT embed the key, the gater would have to fall back on something weaker
// than a fingerprint — and peer.go:28's "exact device-fingerprint allowlist, not
// 'trust the LAN'" would quietly stop being true over libp2p while still being
// true over the LAN path. Hence RequireEd25519: any other key type is refused at
// the handshake, not tolerated with a degraded check.
//
// §2 ML-DSA MIGRATION IMPLICATION — written down now, not implemented now.
// HARDENING-PLAN §16 warns the §2 post-quantum migration "has to be planned
// across both at once", and this function is why. Today's identity is Ed25519 in
// two encodings; a switch to ML-DSA changes both at the same instant:
//
//  1. pool.DeviceID would become sha256(ML-DSA pubkey). Every existing
//     fingerprint in every coordinator record, every config staticPeers entry,
//     and every operator's notes changes value. That is a coordinator-side
//     migration with a dual-fingerprint window, not a connector-side rename.
//  2. libp2p has no ML-DSA key type. cryptopb.KeyType enumerates RSA, Ed25519,
//     Secp256k1 and ECDSA only, and peer IDs are defined over that enum. An
//     ML-DSA public key is also far too large (≈1312+ bytes for ML-DSA-44) to sit
//     inside an identity multihash the way a 32-byte Ed25519 key does, so peer
//     IDs would become sha256-based and ExtractPublicKey would stop working —
//     which removes the directory-free derivation this file depends on.
//  3. Therefore the migration order is forced: the coordinator must be able to
//     serve BOTH fingerprints for a peer before any connector switches, and the
//     libp2p side needs either a hybrid handshake (Ed25519 peer identity carrying
//     an ML-DSA-signed binding to the new DeviceID) or a libp2p release that
//     defines an ML-DSA key type. Until one of those exists, §2 cannot land for
//     the libp2p path even if it lands for the LAN path — and shipping it for one
//     transport only would create exactly the two-sources-of-truth problem this
//     package otherwise avoids.
//
// Nothing here implements any of that. It is recorded so the plan's warning has a
// concrete consequence attached to it instead of being a sentence.

var (
	// ErrKeyType is returned for any non-Ed25519 libp2p identity. See
	// RequireEd25519 above for why this is refused rather than degraded.
	ErrKeyType = errors.New("p2p: only Ed25519 libp2p identities are accepted (the DeviceID mapping requires it)")
	// ErrNoEmbeddedKey means the peer ID did not carry its public key, so no
	// DeviceID can be derived from it without a directory.
	ErrNoEmbeddedKey = errors.New("p2p: peer ID does not embed its public key, so no DeviceID can be derived")
)

// PrivateKey converts a pool.Identity into the libp2p private key for this host,
// so the host's peer ID and its pool.DeviceID describe the same keypair. It does
// NOT generate or persist a key: pool.Identity is explicit that key storage
// belongs to the embedding agent's reviewed policy, and inventing a second key
// here would give one machine two identities.
func PrivateKey(id pool.Identity) (libcrypto.PrivKey, error) {
	if len(id.PrivateKey) != ed25519.PrivateKeySize || len(id.PublicKey) != ed25519.PublicKeySize {
		return nil, ErrInvalid
	}
	// The public half must actually belong to the private half. Without this a
	// caller could hand us a mismatched pair and the host would present a peer
	// ID whose DeviceID is not the DeviceID the rest of the connector uses.
	if !id.PublicKey.Equal(id.PrivateKey.Public()) {
		return nil, ErrInvalid
	}
	return libcrypto.UnmarshalEd25519PrivateKey(id.PrivateKey)
}

// PeerIDFor maps an ed25519 public key to the libp2p peer ID it produces.
func PeerIDFor(publicKey ed25519.PublicKey) (peer.ID, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", ErrInvalid
	}
	pub, err := libcrypto.UnmarshalEd25519PublicKey(publicKey)
	if err != nil {
		return "", err
	}
	return peer.IDFromPublicKey(pub)
}

// PeerIDForDevice is deliberately absent, and its absence is the point.
//
// A DeviceID is sha256(pubkey), which is one-way: you cannot recover the public
// key, so you cannot construct the peer ID from a fingerprint alone. Direction
// matters for authorization. We always go peer.ID → DeviceID on an
// ALREADY-AUTHENTICATED connection and compare against the coordinator's set. We
// never go the other way, because that would require trusting some map from
// fingerprint to key — a second source of truth about identity, and a place to
// inject one. If a caller needs to dial a specific device, the coordinator must
// supply that peer's addresses (see Rendezvous), which is where identity belongs.

// DeviceIDFromPeerID extracts the embedded Ed25519 public key from an
// authenticated peer ID and returns the repo's fingerprint for it.
//
// It refuses every other key type. An RSA or ECDSA peer ID is a sha256 of the
// marshalled key and carries no recoverable key, so no DeviceID exists for it;
// Secp256k1 is embedded but is not the repo's identity algorithm. In all those
// cases the honest answer is "this identity cannot be checked against the
// allowlist", and the caller must then refuse the connection.
func DeviceIDFromPeerID(id peer.ID) (string, error) {
	if err := id.Validate(); err != nil {
		return "", ErrInvalid
	}
	pub, err := id.ExtractPublicKey()
	if err != nil {
		// peer.ErrNoPublicKey for a hashed (non-identity-multihash) peer ID.
		return "", ErrNoEmbeddedKey
	}
	if pub.Type() != cryptopb.KeyType_Ed25519 {
		return "", ErrKeyType
	}
	raw, err := pub.Raw()
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return "", ErrKeyType
	}
	device := pool.DeviceID(ed25519.PublicKey(raw))
	if device == "" {
		return "", ErrKeyType
	}
	return device, nil
}
