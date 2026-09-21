// Package p2p is the libp2p data plane: AutoNAT dialability detection, DCUtR
// hole punching, and Circuit Relay v2 fallback with hard byte/time/connection
// ceilings. It is Phase 2 of TRANSPORT-NAT-DESIGN.md.
//
// WHY A DEPENDENCY EXISTS HERE AND NOWHERE ELSE IN THE TREE. The connector's
// standing rule is hand-roll it, keep zero dependencies. HARDENING-PLAN.md §16
// line 872 selects go-libp2p for the data plane instead. That conflict was put
// to the founder and decided on 2026-09-21 in favour of go-libp2p, an explicit
// override of the zero-dependency rule on the plan's authority. This package is
// the only place that override applies. Do not read it as permission to add
// anything else: every other package in connector/ still compiles against the
// standard library alone, and TRANSPORT-NAT-DESIGN.md §6 records the decision so
// a future contributor cannot mistake it for an accident and revert it.
//
// WHAT THIS PACKAGE IS NOT.
//
//   - It is not a replacement for the LAN mutual-TLS peer path in internal/pool.
//     That path is the proven transport, it is untouched, and it is PREFERRED
//     whenever the peer is reachable on a private address. pool.privateIP and
//     pool.ValidPeerEndpoint are not relaxed by a single line here. libp2p is an
//     ADDITIONAL path for peers that no private address can reach. Two transports
//     coexist; see Snapshot.PreferLAN and doc §10.
//   - It is not membership and it is not authorization. HARDENING-PLAN §30.2:
//     discovery and transport never establish membership. Everything libp2p
//     produces is a dial hint. The coordinator's authorized set alone populates
//     the allowlist this package gates on, exactly as peer.go:28 requires:
//     "AllowedPeers is an exact device-fingerprint allowlist, not 'trust the
//     LAN'". Reachable is not authorized, and being able to complete a libp2p
//     handshake admits nobody.
//   - It is not on by default. Options.Enabled is false unless the owner turns it
//     on (config.P2P.enabled). New returns a disabled Host that starts no
//     listener, dials nothing, and holds no reservation. Shipping this cannot
//     regress an existing user, because for an existing user it does not run.
//   - It does not talk to the coordinator. Rendezvous is an interface with a
//     stub implementation; the endpoints the coordinator must expose are listed
//     on the Rendezvous doc comment and are OUT OF SCOPE here.
package p2p

import "errors"

var (
	// ErrDisabled is returned by every operation on a Host built with
	// Enabled:false. It is a distinct error rather than a silent no-op so a
	// caller cannot mistake "the feature flag is off" for "the peer is
	// unreachable" and start retrying a path that structurally cannot run.
	ErrDisabled = errors.New("p2p: the libp2p data plane is disabled (config.p2p.enabled is false)")
	// ErrUnauthorized means the peer completed a libp2p handshake but its
	// device fingerprint is not in the coordinator-supplied authorized set.
	// This is the §30.2 boundary and it is the only reason the gater needs.
	ErrUnauthorized = errors.New("p2p: peer is reachable but not authorized by the coordinator")
	// ErrInvalid is a rejected argument or a self-inconsistent configuration.
	ErrInvalid = errors.New("p2p: invalid argument")
	// ErrBudget means a relay resource ceiling has been reached. It is
	// deliberately not retryable-looking: the ceiling is the cost control and a
	// caller that treats it as transient defeats the point.
	ErrBudget = errors.New("p2p: relay resource ceiling reached")
	// ErrNoRendezvous means signalling is not wired up. Phase 2 stubs the
	// coordinator side behind Rendezvous, so this is the expected error until
	// the coordinator exposes the endpoints listed on that interface.
	ErrNoRendezvous = errors.New("p2p: no rendezvous/signalling channel is configured (coordinator side not built)")
)
