package p2p

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"

	"nexal/connector/internal/stun"
)

func stunResult(m stun.Mapping) *stun.Result {
	return &stun.Result{
		Reachable: true, Mapping: m, MappingLabel: m.String(), Summary: m.Summary(),
		Reflexive: netip.MustParseAddrPort("203.0.113.9:45039"),
	}
}

// TestNATSignalsAreNotTwoSourcesOfTruth is the reconciliation requirement: STUN
// and AutoNAT must not become two contradictory answers about NAT class. The rule
// is that each answers only its own question, so the test asserts the composed
// conclusions for every combination rather than asserting a winner.
func TestNATSignalsAreNotTwoSourcesOfTruth(t *testing.T) {
	cases := []struct {
		name      string
		reach     network.Reachability
		seen      bool
		mapping   *stun.Result
		punch     bool
		relay     bool
		agreement string
	}{
		// Public: mapping behaviour is IRRELEVANT to inbound dialability, which is
		// the clearest demonstration that these are different questions.
		{"public + symmetric", network.ReachabilityPublic, true, stunResult(stun.MappingEndpointDependent), false, false, "complementary"},
		{"public + no stun", network.ReachabilityPublic, true, nil, false, false, "complementary"},
		// Both signals independently point at a relay. This is the only "agree".
		{"private + symmetric", network.ReachabilityPrivate, true, stunResult(stun.MappingEndpointDependent), false, true, "agree"},
		// The common home case: not dialable but punchable. NOT a conflict.
		{"private + endpoint-independent", network.ReachabilityPrivate, true, stunResult(stun.MappingEndpointIndependent), true, false, "complementary"},
		{"private + no stun", network.ReachabilityPrivate, true, nil, true, false, "complementary"},
		// STUN alone is enough to skip a punch that cannot work.
		{"unknown + symmetric", network.ReachabilityUnknown, false, stunResult(stun.MappingEndpointDependent), false, true, "complementary"},
		// Nothing measured: conclude nothing, try the cheap path first.
		{"nothing measured", network.ReachabilityUnknown, false, nil, true, false, "insufficient"},
		{"stun unreachable", network.ReachabilityUnknown, false, &stun.Result{}, true, false, "insufficient"},
	}
	for _, c := range cases {
		d := Reconcile(c.reach, c.seen, c.mapping)
		if d.PunchWorthAttempting != c.punch || d.RelayLikelyRequired != c.relay || d.Agreement != c.agreement {
			t.Fatalf("%s: punch=%v relay=%v agreement=%q, want %v/%v/%q",
				c.name, d.PunchWorthAttempting, d.RelayLikelyRequired, d.Agreement, c.punch, c.relay, c.agreement)
		}
		if d.Summary == "" {
			t.Fatalf("%s: no summary", c.name)
		}
		// Phase 1's rule, still enforced: no surface may claim a punch will work.
		// Only a completed direct connection proves that, and PunchStats reports
		// those separately.
		for _, forbidden := range []string{"hole punching will work", "will succeed", "guaranteed"} {
			if strings.Contains(strings.ToLower(d.Summary), forbidden) {
				t.Fatalf("%s: summary overclaims (%q): %s", c.name, forbidden, d.Summary)
			}
		}
		// A punch and a "relay required" must never both be asserted: that would be
		// the contradictory-sources-of-truth failure this file exists to prevent.
		if d.PunchWorthAttempting && d.RelayLikelyRequired {
			t.Fatalf("%s: contradictory conclusion", c.name)
		}
	}
}

// TestSTUNClassificationIsNotRestated: the labels come straight from
// internal/stun so there is one vocabulary for NAT class in the tree. If someone
// adds a second spelling here, this fails.
func TestSTUNClassificationIsNotRestated(t *testing.T) {
	for _, m := range []stun.Mapping{stun.MappingUnknown, stun.MappingEndpointIndependent, stun.MappingEndpointDependent} {
		d := Reconcile(network.ReachabilityPrivate, true, stunResult(m))
		want := m.String()
		if m == stun.MappingUnknown {
			// An unreachable/unknown STUN result is reported as unobserved rather
			// than as a measured "unknown", because those differ.
			want = stun.MappingUnknown.String()
		}
		if d.MappingLabel != want {
			t.Fatalf("mapping label %q, want %q", d.MappingLabel, want)
		}
	}
	d := Reconcile(network.ReachabilityPrivate, true, nil)
	if d.STUNObserved {
		t.Fatal("STUNObserved true with no STUN result")
	}
}

// TestDisabledHostReportsNoAutoNATVerdict: "not measured" and "measured as
// unreachable" lead to different decisions, so they must not render the same.
func TestDisabledHostReportsNoAutoNATVerdict(t *testing.T) {
	h, err := New(Options{Enabled: false, STUN: stunResult(stun.MappingEndpointIndependent)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := h.Dialability()
	if d.AutoNATObserved {
		t.Fatal("a disabled host claimed an AutoNAT verdict")
	}
	if !strings.Contains(d.Summary, "disabled") {
		t.Fatalf("summary does not say the data plane is off: %s", d.Summary)
	}
	// The Phase 1 STUN observation stays readable with the flag off, which is the
	// point of keeping it as the cheap pre-check rather than folding it into libp2p.
	if !d.STUNObserved || d.MappingLabel != stun.MappingEndpointIndependent.String() {
		t.Fatalf("STUN pre-check lost when libp2p is disabled: %+v", d)
	}
}
