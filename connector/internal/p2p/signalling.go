package p2p

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// THE COORDINATOR SEAM — DEFINED HERE, NOT BUILT HERE.
//
// TRANSPORT-NAT-DESIGN.md §5 lists the three things an internet peer connection
// needs: reflexive addresses (Phase 1), a SIGNALLING CHANNEL that carries each
// side's candidates to the other, and a simultaneous dial. libp2p supplies the
// third (DCUtR) and most of the first. It does NOT supply the second for a closed
// membership system: DCUtR runs OVER an existing relayed connection, so both peers
// must already know a relay and each other's addresses before DCUtR can start.
// In public IPFS that bootstrap comes from the DHT. We must not use the DHT —
// joining a global network would publish our members' addresses to strangers and
// would import a discovery mechanism that §30.2 forbids from carrying any
// authorization weight anyway.
//
// So the bootstrap must come from the coordinator, and the coordinator side is
// explicitly OUT OF SCOPE for this change. This interface is the seam. Everything
// behind it is stubbed, and Host works with a stub: with no Rendezvous, the host
// starts, gates connections, serves as a relay if configured, and accepts inbound
// peers — it simply cannot INITIATE a connection to a peer it has no address for.
//
// WHAT THE COORDINATOR MUST EXPOSE. Four endpoints, and the shapes are the
// methods below. They are additive to the existing peer directory, and none of
// them may carry authorization: §30.2 still holds, so the directory's Authorized
// flag remains the only thing that admits a peer, and these endpoints must be
// refused for any caller whose own device is not authorized.
//
//  1. POST /v1/peers/self/p2p — publish this device's libp2p peer ID, its
//     advertised multiaddrs, and its relay reservation addresses. Phase 1
//     deliberately published nothing; this is the point at which an address
//     leaves the machine, so it must be gated on the owner's feature flag and
//     must never include addresses the host has not actually bound.
//  2. GET /v1/peers/{deviceId}/p2p — fetch one authorized peer's peer ID and
//     multiaddrs. Returns 404 for a device the caller is not authorized to
//     transfer with, so the endpoint cannot be used to enumerate membership.
//  3. GET /v1/relays — the set of relay candidates: authorized, publicly dialable
//     donor devices that have opted into relay duty. §16 requires these be donors
//     and be paid, never company-run infrastructure, so this list is the
//     coordinator's, not a hardcoded bootstrap list in the binary.
//  4. DELETE /v1/peers/self/p2p — withdraw the published addresses on shutdown or
//     when the owner turns the feature off. Without this, a stale address set
//     keeps being handed to peers that will fail to dial it.
//
// The coordinator must also enforce one property this package cannot: a peer's
// PUBLISHED peer ID has to match the DeviceID it published under. The check is
// DeviceIDFromPeerID(peerID) == deviceId, four lines, and without it a member
// could publish someone else's peer ID and redirect their traffic. This package
// re-checks it on every fetch (see stubRendezvous.Peer and Host.Connect), because
// a client that trusts the server's pairing is a client with a second source of
// truth about identity — but the coordinator should reject it at write time too.

// PeerRecord is one peer's libp2p addressing, as the coordinator would return it.
type PeerRecord struct {
	DeviceID string   `json:"deviceId"`
	PeerID   string   `json:"peerId"`
	Addrs    []string `json:"addrs"`
}

// Rendezvous is the signalling channel. It is an interface so this package holds
// no coordinator client and cannot acquire one by accident.
type Rendezvous interface {
	// Publish advertises this host's peer ID and addresses.
	Publish(ctx context.Context, rec PeerRecord) error
	// Peer resolves an authorized peer's addressing by device fingerprint.
	Peer(ctx context.Context, deviceID string) (peer.AddrInfo, error)
	// Relays lists relay candidates: authorized, publicly dialable donors.
	Relays(ctx context.Context) ([]peer.AddrInfo, error)
	// Withdraw removes this host's published addresses.
	Withdraw(ctx context.Context) error
}

// StubRendezvous is the in-process implementation. It is not a mock that pretends
// to be the coordinator — it is a local table with no transport at all, which is
// what lets the Phase 2 tests exercise connect/relay/punch paths offline while
// keeping the coordinator work honestly unbuilt.
//
// It ENFORCES the peer-ID-matches-DeviceID rule, so the tests that assert the rule
// exercise the same code path production will.
type StubRendezvous struct {
	mu        sync.Mutex
	records   map[string]PeerRecord
	relays    []peer.AddrInfo
	published *PeerRecord
}

func NewStubRendezvous() *StubRendezvous {
	return &StubRendezvous{records: make(map[string]PeerRecord)}
}

func (s *StubRendezvous) Publish(_ context.Context, rec PeerRecord) error {
	if err := checkRecord(rec); err != nil {
		return err
	}
	s.mu.Lock()
	copied := rec
	s.published = &copied
	s.records[rec.DeviceID] = rec
	s.mu.Unlock()
	return nil
}

func (s *StubRendezvous) Peer(_ context.Context, deviceID string) (peer.AddrInfo, error) {
	s.mu.Lock()
	rec, ok := s.records[deviceID]
	s.mu.Unlock()
	if !ok {
		return peer.AddrInfo{}, ErrNoRendezvous
	}
	return addrInfo(rec)
}

func (s *StubRendezvous) Relays(context.Context) ([]peer.AddrInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]peer.AddrInfo(nil), s.relays...), nil
}

func (s *StubRendezvous) Withdraw(context.Context) error {
	s.mu.Lock()
	s.published = nil
	s.mu.Unlock()
	return nil
}

// SetRelays seeds relay candidates. Test/wiring helper; the real implementation
// reads GET /v1/relays.
func (s *StubRendezvous) SetRelays(relays ...peer.AddrInfo) {
	s.mu.Lock()
	s.relays = append([]peer.AddrInfo(nil), relays...)
	s.mu.Unlock()
}

// Published returns what this host last advertised, so a test can assert that a
// disabled host publishes nothing.
func (s *StubRendezvous) Published() *PeerRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published == nil {
		return nil
	}
	copied := *s.published
	return &copied
}

// checkRecord is the identity-pairing rule described above: a record's peer ID
// must derive to the DeviceID it claims. Applied on write AND on read, because the
// only thing worse than the coordinator not checking is us assuming it did.
func checkRecord(rec PeerRecord) error {
	if !validFingerprint(rec.DeviceID) {
		return ErrInvalid
	}
	id, err := peer.Decode(rec.PeerID)
	if err != nil {
		return ErrInvalid
	}
	device, err := DeviceIDFromPeerID(id)
	if err != nil {
		return err
	}
	if device != rec.DeviceID {
		return ErrUnauthorized
	}
	return nil
}

func addrInfo(rec PeerRecord) (peer.AddrInfo, error) {
	if err := checkRecord(rec); err != nil {
		return peer.AddrInfo{}, err
	}
	id, err := peer.Decode(rec.PeerID)
	if err != nil {
		return peer.AddrInfo{}, ErrInvalid
	}
	info := peer.AddrInfo{ID: id}
	for _, a := range rec.Addrs {
		m, err := multiaddrOf(a)
		if err != nil {
			// A single unparseable address is dropped, not fatal: a coordinator
			// that learns a new multiaddr protocol before this binary does must
			// not make the whole record unusable.
			continue
		}
		info.Addrs = append(info.Addrs, m)
	}
	return info, nil
}
