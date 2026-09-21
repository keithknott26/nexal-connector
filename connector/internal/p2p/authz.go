package p2p

import (
	"net"
	"sort"
	"sync"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// THE §30.2 BOUNDARY, IMPLEMENTED AS A GATE THAT CANNOT BE BYPASSED.
//
// HARDENING-PLAN §30.2: discovery and transport never establish membership or
// authorization. internal/discovery already says a candidate "is a hint about
// where to look. It is never evidence of membership". libp2p makes that rule
// harder to keep, because libp2p is very good at finding peers and at completing
// a mutually authenticated handshake with them — and a completed handshake feels
// like an admission decision. It is not one. A libp2p handshake proves the remote
// holds a private key. It proves nothing about whether the coordinator has
// authorized that device.
//
// So the allowlist is a HARD GATE at the libp2p layer, not a check somewhere
// further up the call stack that a later refactor could route around:
//
//   - Authorizer is the only source of truth, and it is fed from the coordinator's
//     authorized set alone (pool.Peer.Authorized, exactly as the LAN path does).
//     There is no "configured therefore allowed", no "discovered therefore
//     allowed", and no "reachable therefore allowed".
//   - The gate runs in InterceptSecured, which is the FIRST moment a peer ID is
//     authenticated and is BEFORE the muxer is negotiated, so an unauthorized peer
//     cannot open a stream at all — not one byte of application data crosses. It
//     runs again in InterceptUpgraded as a belt-and-braces check with an explicit
//     disconnect reason, because a transport that hands libp2p an
//     already-secure connection (QUIC) reaches the upgrade hook by a different
//     path.
//   - An empty allowlist denies everything. That is the safe direction for a
//     feature whose config may be half-written, and it is asserted by a test. The
//     LAN path takes the same position: pool.newPeerPolicy refuses len(allowed)<1.
//
// The mirror-image rule also holds and is tested: this gate NEVER ADDS to the
// allowlist. Nothing in this package can make a peer authorized. It can only
// refuse one.

// Authorizer answers exactly one question and deliberately cannot answer any
// other: is this device fingerprint in the coordinator's authorized set.
//
// It is an interface so the agent can hand in a live view of the coordinator's
// directory without this package learning how to talk to the coordinator. A
// package that could fetch the allowlist itself would be a package that could
// decide to widen it.
type Authorizer interface {
	Authorized(deviceID string) bool
	// Snapshot is for surfaces only (doctor/status). It returns a copy.
	Snapshot() []string
}

// CoordinatorAuthorizer holds the coordinator-supplied authorized fingerprints.
// Replace wholesale on each directory refresh; there is no Add, on purpose —
// incremental mutation is how a stale entry survives a revocation.
type CoordinatorAuthorizer struct {
	mu      sync.RWMutex
	allowed map[string]struct{}
}

// NewCoordinatorAuthorizer builds the set. Entries that are not 64 lowercase hex
// characters are dropped rather than normalised: a fingerprint we had to fix up
// is a fingerprint we are not certain about, and uppercase-vs-lowercase drift is
// precisely the bug that makes an exact allowlist stop being exact.
func NewCoordinatorAuthorizer(fingerprints []string) *CoordinatorAuthorizer {
	a := &CoordinatorAuthorizer{}
	a.Replace(fingerprints)
	return a
}

// Replace swaps the whole set, so a revoked device disappears on the next refresh.
func (a *CoordinatorAuthorizer) Replace(fingerprints []string) {
	next := make(map[string]struct{}, len(fingerprints))
	for _, f := range fingerprints {
		if validFingerprint(f) {
			next[f] = struct{}{}
		}
	}
	a.mu.Lock()
	a.allowed = next
	a.mu.Unlock()
}

func (a *CoordinatorAuthorizer) Authorized(deviceID string) bool {
	if !validFingerprint(deviceID) {
		return false
	}
	a.mu.RLock()
	_, ok := a.allowed[deviceID]
	a.mu.RUnlock()
	return ok
}

func (a *CoordinatorAuthorizer) Snapshot() []string {
	a.mu.RLock()
	out := make([]string, 0, len(a.allowed))
	for f := range a.allowed {
		out = append(out, f)
	}
	a.mu.RUnlock()
	sort.Strings(out)
	return out
}

// validFingerprint duplicates the shape check rather than importing
// internal/config's unexported one, for the same reason config duplicates
// pool.ValidPeerEndpoint: importing across those packages is a cycle. The shape
// is four lines and fully covered by a test, so a comment is not carrying the
// weight here.
func validFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Denial is one refused connection, kept for the owner-facing surface. An
// authorization gate that refuses silently is a gate that generates support
// tickets nobody can diagnose.
type Denial struct {
	Peer     string `json:"peer"`
	DeviceID string `json:"deviceId,omitempty"`
	Reason   string `json:"reason"`
	Relayed  bool   `json:"relayed"`
}

// gater is the connmgr.ConnectionGater implementation. It owns two independent
// jobs that both have to happen at connection time, which is why they share a
// type: the §30.2 authorization gate, and the relay CONNECTION-COUNT ceiling
// (the other two ceilings, time and bytes, cannot be decided at this checkpoint —
// see limits.go).
type gater struct {
	authz  Authorizer
	ledger *Ledger

	mu      sync.Mutex
	denials []Denial
	denied  uint64
}

const maxRecordedDenials = 32

func newGater(authz Authorizer, ledger *Ledger) *gater {
	return &gater{authz: authz, ledger: ledger}
}

func (g *gater) deny(p peer.ID, device, reason string, relayed bool) {
	g.mu.Lock()
	g.denied++
	if len(g.denials) == maxRecordedDenials {
		// Keep the most recent, drop the oldest: an attacker who can generate
		// denials must not be able to push the interesting one out of a log the
		// owner is about to read, and must not be able to grow our heap either.
		copy(g.denials, g.denials[1:])
		g.denials = g.denials[:maxRecordedDenials-1]
	}
	g.denials = append(g.denials, Denial{Peer: p.String(), DeviceID: device, Reason: reason, Relayed: relayed})
	g.mu.Unlock()
}

// Denials returns a copy of the recent refusals and the total count.
func (g *gater) Denials() ([]Denial, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Denial(nil), g.denials...), g.denied
}

// InterceptPeerDial cannot check authorization usefully in the general case —
// but it can, because our peer IDs embed their keys, so we check here too. A dial
// we should never make is cheapest to stop before address resolution.
func (g *gater) InterceptPeerDial(p peer.ID) bool { return g.allowPeer(p, false) }

// InterceptAddrDial additionally applies the relay circuit ceiling to OUTBOUND
// relayed dials. This is the cost control's first line: the ceiling has to bite
// on dials we initiate, not only on circuits others open to us, or the budget
// only constrains half the traffic.
func (g *gater) InterceptAddrDial(p peer.ID, addr ma.Multiaddr) bool {
	if !g.allowPeer(p, Relayed(addr)) {
		return false
	}
	if Relayed(addr) && !g.ledger.CircuitAvailable() {
		g.deny(p, "", "relay circuit ceiling reached", true)
		return false
	}
	return true
}

// InterceptAccept runs before any handshake, so no peer ID exists yet and NO
// AUTHORIZATION DECISION IS POSSIBLE OR ATTEMPTED here. It only applies the
// relay circuit ceiling, which is address-shaped and needs no identity.
func (g *gater) InterceptAccept(cm network.ConnMultiaddrs) bool {
	if Relayed(cm.RemoteMultiaddr()) && !g.ledger.CircuitAvailable() {
		g.deny("", "", "relay circuit ceiling reached", true)
		return false
	}
	return true
}

// InterceptSecured is the real gate: the peer ID is authenticated, the muxer is
// not yet negotiated, so refusing here means no stream can ever open.
func (g *gater) InterceptSecured(_ network.Direction, p peer.ID, cm network.ConnMultiaddrs) bool {
	relayed := cm != nil && Relayed(cm.RemoteMultiaddr())
	return g.allowPeer(p, relayed)
}

func (g *gater) InterceptUpgraded(c network.Conn) (bool, control.DisconnectReason) {
	if !g.allowPeer(c.RemotePeer(), Relayed(c.RemoteMultiaddr())) {
		return false, 0
	}
	return true, 0
}

// allowPeer is the single implementation of the §30.2 rule. Every hook funnels
// through it so there is exactly one place where "authorized" is decided.
func (g *gater) allowPeer(p peer.ID, relayed bool) bool {
	if g.authz == nil {
		// No allowlist source means deny, never allow. A nil Authorizer is a
		// wiring bug, and the failure mode of a wiring bug must be "nothing
		// connects", not "everything connects".
		g.deny(p, "", "no coordinator allowlist is wired up", relayed)
		return false
	}
	if p == "" {
		// Identity not yet known at this checkpoint; the decision is deferred to
		// InterceptSecured, which always runs. Returning true here is not an
		// allow, it is "no opinion available yet".
		return true
	}
	device, err := DeviceIDFromPeerID(p)
	if err != nil {
		g.deny(p, "", "peer identity is not an Ed25519 key, so no device fingerprint can be derived", relayed)
		return false
	}
	if !g.authz.Authorized(device) {
		g.deny(p, device, "not in the coordinator's authorized set", relayed)
		return false
	}
	return true
}

// Relayed reports whether a multiaddr traverses a Circuit Relay v2 hop. Exported
// because the byte ceiling, the surfaces and the LAN-preference rule all need the
// same answer, and three copies of a protocol-code check would drift.
func Relayed(addr ma.Multiaddr) bool {
	if addr == nil {
		return false
	}
	_, err := addr.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}

// PrivateAddr reports whether a multiaddr names an address the EXISTING LAN
// mutual-TLS path could carry instead.
//
// It exists to keep the two transports in their lanes rather than to permit
// anything: when this is true for a peer, the LAN path is preferred and libp2p
// should not be used, because the LAN path is the proven one. It intentionally
// mirrors the SHAPE of pool.privateIP (private, loopback or link-local unicast
// unicast addresses) without importing it — pool is imported here only for
// DeviceID, and this function must never become a place where someone "fixes"
// privateIP by loosening a copy of it. The authoritative rule for what the LAN
// transport will dial stays pool.ValidPeerEndpoint; this is only a routing hint.
func PrivateAddr(addr ma.Multiaddr) bool {
	if addr == nil || Relayed(addr) {
		return false
	}
	// manet.IsPrivateAddr exists but excludes loopback, and loopback is one of
	// the three classes pool.privateIP accepts (it is what the tests dial), so the
	// classes are spelled out rather than borrowed approximately.
	for _, p := range []int{ma.P_IP4, ma.P_IP6} {
		v, err := addr.ValueForProtocol(p)
		if err != nil {
			continue
		}
		ip := net.ParseIP(v)
		return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() &&
			(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
	}
	// A name-based multiaddr is not a private address for routing purposes: the
	// LAN path performs no DNS lookup at all (pool sets Proxy:nil and dials the
	// literal address), so a /dns4 peer is not one the LAN path can carry.
	return false
}
