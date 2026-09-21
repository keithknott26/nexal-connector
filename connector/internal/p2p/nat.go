package p2p

import (
	"github.com/libp2p/go-libp2p/core/network"

	"nexal/connector/internal/stun"
)

// ONE SOURCE OF TRUTH ABOUT NAT, NOT TWO.
//
// internal/stun already classifies this host: MappingEndpointIndependent,
// MappingEndpointDependent, or MappingUnknown, measured from two servers on one
// local UDP socket. libp2p's AutoNAT answers a DIFFERENT question:
// ReachabilityPublic / ReachabilityPrivate / ReachabilityUnknown, measured by
// asking peers to dial us back on our advertised addresses.
//
// It would be easy, and wrong, to treat these as two opinions on one fact and let
// whichever ran last win. They are not the same fact:
//
//	STUN    — "does my NAT reuse one mapping across destinations?" That is a
//	          property of the NAT's PORT ALLOCATION. It predicts whether HOLE
//	          PUNCHING can work. It says nothing about whether anyone can reach
//	          an unsolicited inbound connection.
//	AutoNAT — "can a peer complete an inbound dial to an address I advertised?"
//	          That is a property of REACHABILITY. It predicts whether we need a
//	          relay at all. It says nothing about mapping behaviour, so it cannot
//	          predict whether a punch between two private hosts will succeed.
//
// So AutoNAT does NOT supersede STUN and STUN does NOT supersede AutoNAT. They
// compose, and this file is the only place the composition is written down:
//
//   - AutoNAT is AUTHORITATIVE on dialability. It is the stronger signal because
//     it is an end-to-end test by a real third party rather than an inference,
//     and it is the signal libp2p itself acts on when deciding to seek a relay.
//   - STUN is a CHEAP PRE-CHECK on punchability, and it is the only signal
//     available BEFORE any libp2p host exists — including when the feature flag is
//     off, which is the default. `nexal doctor --stun` keeps working exactly as it
//     did in Phase 1 and its meaning is unchanged.
//   - When AutoNAT says private and STUN says endpoint-dependent, the two AGREE on
//     the operational conclusion (relay required) by two independent routes, and
//     Dialability says so with high confidence.
//   - When they point different ways, the disagreement is REPORTED, not resolved
//     by precedence. "AutoNAT says private, STUN says endpoint-independent" is the
//     normal, common state of a NATed host that can still punch — it is not a
//     contradiction and must not be printed as one.
//
// The rule that prevents two sources of truth is narrower than "pick a winner":
// each signal answers only its own question, and no consumer is allowed to read a
// punchability conclusion out of AutoNAT or a dialability conclusion out of STUN.
// Dialability is the single type consumers read, so there is nowhere to make that
// mistake.

// Dialability is the reconciled view. It is a value, not a live object, so a
// surface prints a coherent snapshot rather than two fields sampled a second
// apart.
type Dialability struct {
	// Reachability is AutoNAT's answer, the authority on inbound dialability.
	// ReachabilityUnknown when the libp2p host is disabled or has not yet
	// received an AutoNAT verdict — never silently rendered as private, because
	// "not measured" and "measured as unreachable" lead to different decisions.
	Reachability network.Reachability `json:"-"`
	// ReachabilityLabel is Reachability as a string, for JSON surfaces.
	ReachabilityLabel string `json:"reachability"`
	// AutoNATObserved is true once a real AutoNAT verdict has arrived. It is the
	// difference between "Unknown because unmeasured" and "Unknown because
	// AutoNAT itself could not decide".
	AutoNATObserved bool `json:"autonatObserved"`

	// Mapping is internal/stun's classification, the pre-check on punchability.
	Mapping      stun.Mapping `json:"-"`
	MappingLabel string       `json:"mapping"`
	// STUNObserved is true when a STUN result was supplied at all. Phase 1's
	// observation is opt-in, so absent is the common case.
	STUNObserved bool `json:"stunObserved"`

	// PunchWorthAttempting is the composed operational answer, and it is
	// deliberately optimistic-by-default in exactly one direction: an UNKNOWN
	// mapping still attempts a punch, because the punch is cheap and the relay is
	// the expensive path. Only a MEASURED endpoint-dependent NAT skips straight
	// to the relay.
	PunchWorthAttempting bool `json:"punchWorthAttempting"`
	// RelayLikelyRequired is true when the evidence points at needing a relay.
	RelayLikelyRequired bool `json:"relayLikelyRequired"`
	// Agreement describes how the two signals relate. Values: "agree",
	// "complementary", "insufficient".
	Agreement string `json:"agreement"`
	// Summary is the honest sentence for an owner-facing surface. Like
	// stun.Mapping.Summary it never claims a hole punch will work; only a
	// completed direct connection proves that, and Snapshot reports those
	// separately.
	Summary string `json:"summary"`
}

// Reconcile composes the two signals. It takes the STUN result by value and
// tolerates its absence, because the STUN probe is opt-in and this must produce a
// usable answer without it.
func Reconcile(r network.Reachability, autonatObserved bool, s *stun.Result) Dialability {
	d := Dialability{
		Reachability:      r,
		ReachabilityLabel: r.String(),
		AutoNATObserved:   autonatObserved,
		Mapping:           stun.MappingUnknown,
		MappingLabel:      stun.MappingUnknown.String(),
	}
	if s != nil && s.Reachable {
		d.Mapping = s.Mapping
		d.MappingLabel = s.Mapping.String()
		d.STUNObserved = true
	}

	switch {
	case r == network.ReachabilityPublic:
		// Publicly dialable: no punch and no relay needed for INBOUND. Mapping
		// behaviour is irrelevant here, which is the clearest case of the two
		// signals answering different questions — a public host can have any
		// mapping behaviour and it does not matter.
		d.PunchWorthAttempting = false
		d.RelayLikelyRequired = false
		d.Agreement = "complementary"
		d.Summary = "AutoNAT reports this host is publicly dialable, so peers can connect directly without a punch or a relay; NAT mapping behaviour does not affect inbound dialability and is not consulted"
	case d.Mapping == stun.MappingEndpointDependent:
		// The one case where a punch is NOT worth attempting. Endpoint-dependent
		// (symmetric/CGNAT) defeats DCUtR, which HARDENING-PLAN §16 states
		// outright, so going straight to the relay saves a guaranteed failure.
		d.PunchWorthAttempting = false
		d.RelayLikelyRequired = true
		if r == network.ReachabilityPrivate {
			d.Agreement = "agree"
			d.Summary = "AutoNAT reports this host is not publicly dialable and STUN independently measured a per-destination NAT mapping (endpoint-dependent/symmetric); both point at the same conclusion, so a relayed fallback is required for peers this host cannot reach on the LAN"
		} else {
			d.Agreement = "complementary"
			d.Summary = "STUN measured a per-destination NAT mapping (endpoint-dependent/symmetric), which defeats hole punching regardless of what AutoNAT concludes; a relayed fallback is required, and AutoNAT has not (yet) contradicted that"
		}
	case r == network.ReachabilityPrivate:
		// The common home-network state. Not dialable, but the mapping may still
		// permit a punch — this is complementary information, not a conflict, and
		// saying "conflict" here would train an owner to ignore the field.
		d.PunchWorthAttempting = true
		d.RelayLikelyRequired = false
		d.Agreement = "complementary"
		if d.Mapping == stun.MappingEndpointIndependent {
			d.Summary = "AutoNAT reports this host is not publicly dialable, and STUN measured one NAT mapping reused across destinations (endpoint-independent); these answer different questions and do not conflict — DCUtR hole punching is worth attempting, and the relay stays a fallback"
		} else {
			d.Summary = "AutoNAT reports this host is not publicly dialable and no STUN mapping measurement is available; DCUtR hole punching is still attempted first because a punch is cheap and a relay is the expensive path"
		}
	default:
		d.PunchWorthAttempting = true
		d.RelayLikelyRequired = false
		d.Agreement = "insufficient"
		d.Summary = "dialability is unknown: AutoNAT has not returned a verdict" +
			map[bool]string{true: " and STUN could not classify the NAT", false: " and no STUN measurement was supplied"}[d.STUNObserved] +
			"; nothing is concluded, and a direct connection is attempted before a relay"
	}
	return d
}
