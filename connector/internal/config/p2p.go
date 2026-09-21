package config

import "errors"

// P2P is the owner's switch and ceilings for the libp2p data plane
// (internal/p2p, TRANSPORT-NAT-DESIGN.md Phase 2).
//
// ABSENT MEANS OFF, and that is the shipping requirement rather than a
// preference: a binary upgrade must not start a new transport, bind a new socket,
// publish an address, or carry another peer's bytes on an owner's link. Every
// existing config on disk omits this block, so every existing user gets
// Enabled:false. omitempty keeps it omitted on rewrite, exactly as Discovery,
// StaticPeers and the upload fields do.
//
// WHY THE CEILINGS LIVE IN THE CONFIG AT ALL. HARDENING-PLAN lines 877-879 make
// the byte ceiling the cost control for the relayed fallback — "a fallback path
// cannot silently run up spend" — and §16 adds that a donor's own ISP cap (often
// ~1.2 TB/month) is a churn risk. A ceiling the owner cannot see or change is not
// a control, it is a constant. So these are config fields, and `nexal doctor`
// prints the usage against them.
//
// A ZERO IS NOT UNLIMITED. Every numeric field treats zero as "use the reviewed
// default" (see p2p.Limits.Normalize), because the most likely way a cap gets
// removed is an owner or a migration writing 0 into it. There is no value that
// means unlimited, on purpose.
type P2P struct {
	// Enabled is the feature flag for the whole libp2p path.
	Enabled bool `json:"enabled"`
	// Listen are libp2p multiaddrs to bind. Empty means loopback only
	// (p2p.DefaultListen), which is the safe default for a feature whose purpose
	// is reaching the internet: an owner who wants a routable bind must say so.
	Listen []string `json:"listen,omitempty"`
	// MaxRelayedConns caps simultaneous relayed circuits. Direct and LAN
	// connections are not counted; they cost nobody anything.
	MaxRelayedConns int `json:"maxRelayedConns,omitempty"`
	// MaxCircuitSeconds caps one relayed circuit's wall-clock life.
	MaxCircuitSeconds uint64 `json:"maxCircuitSeconds,omitempty"`
	// MaxCircuitBytes caps bytes on one relayed circuit.
	MaxCircuitBytes uint64 `json:"maxCircuitBytes,omitempty"`
	// MaxTotalBytes caps relayed bytes across all circuits for this process run.
	// It is the ceiling that actually bounds spend, because a per-circuit cap
	// alone is defeated by opening more circuits. It is NOT the per-donor monthly
	// budget §16 requires — that needs persistence the connector does not have —
	// and p2p.Limits.Stated() says so on every surface that prints it.
	MaxTotalBytes uint64 `json:"maxTotalBytes,omitempty"`
	// OfferRelayService volunteers this host as a Circuit Relay v2 for other
	// authorized peers: the "publicly dialable donors, paid for it" role in §16.
	// Default false, and it only takes effect if AutoNAT confirms this host is
	// publicly dialable.
	OfferRelayService bool `json:"offerRelayService,omitempty"`
	// MaxServiceReservations caps how many peers may reserve a slot on us.
	MaxServiceReservations int `json:"maxServiceReservations,omitempty"`
}

// P2PEnabled is nil-safe so callers do not have to repeat the check.
func (p *P2P) P2PEnabled() bool { return p != nil && p.Enabled }

// Validate rejects a contradictory block. The RANGE checks live in
// p2p.Limits.Normalize so there is one table of bounds; this catches the two
// errors that are config-shaped rather than limit-shaped.
func (p *P2P) Validate() error {
	if p == nil {
		return nil
	}
	if p.OfferRelayService && !p.Enabled {
		return errors.New("p2p.offerRelayService requires p2p.enabled; volunteering to relay a peer's bytes over a disabled transport cannot work")
	}
	if len(p.Listen) > 8 {
		return errors.New("p2p.listen accepts at most 8 multiaddrs")
	}
	for _, a := range p.Listen {
		if a == "" || len(a) > 128 || a[0] != '/' {
			return errors.New("each p2p.listen entry must be a multiaddr beginning with /")
		}
	}
	return nil
}
