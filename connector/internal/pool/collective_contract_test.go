package pool

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The ring's wire format crosses no repository boundary: both ends of a frame are
// this same Go module, so a fixture on this side is the only side there is. One
// value it uses does cross the boundary, though — the 64-hex device fingerprint a
// rank is identified by comes from the coordinator's host directory. §43's rule
// applies to it: read the OTHER side's source, because a fixture written here
// cannot detect the other side loosening its shape.
func platformFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("../../../../nexal-platform/" + path)
	if err != nil {
		t.Skipf("platform source not checked out alongside this repo: %v", err)
	}
	return string(b)
}

func TestRingFingerprintShapeMatchesCoordinatorSource(t *testing.T) {
	src := platformFile(t, "apps/coordinator/src/peers.ts")
	// The literal the coordinator validates an advertised fingerprint with, taken
	// from its source rather than restated here.
	m := regexp.MustCompile(`/\^\[0-9a-f\]\{(\d+)\}\$/`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no ^[0-9a-f]{n}$ fingerprint pattern found in peers.ts; it may have changed shape")
	}
	length, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile("^[0-9a-f]{" + m[1] + "}$")
	probes := []string{
		strings.Repeat("a", length),
		strings.Repeat("A", length),
		strings.Repeat("a", length-1),
		strings.Repeat("a", length+1),
		strings.Repeat("a", length-1) + "g",
		"studio.local",
		"",
	}
	for _, probe := range probes {
		if pattern.MatchString(probe) != validDigest(probe) {
			t.Errorf("coordinator and connector disagree about %q: coordinator accepts=%v, pool accepts=%v",
				probe, pattern.MatchString(probe), validDigest(probe))
		}
	}
	// A ring member is one of these values, so the ring's own member check must
	// agree with it too.
	if _, err := resolveRingMembers(RingOptions{Tenant: testTenant,
		AuthorizedPeers: []string{strings.Repeat("A", length)},
		Members: []RingMember{{Fingerprint: strings.Repeat("a", length), Tenant: testTenant},
			{Fingerprint: strings.Repeat("A", length), Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"}}},
		strings.Repeat("a", length)); err == nil {
		t.Error("the ring accepted an uppercase fingerprint the coordinator would reject")
	}
	// The database-level corruption guard must not be looser than the regex,
	// because the connector trusts rows that reached the directory through it.
	migration := platformFile(t, "apps/coordinator/migrations/0013_peer_rendezvous.sql")
	if !strings.Contains(migration, "length(NEW.device_fingerprint) <> "+m[1]) ||
		!strings.Contains(migration, "GLOB '*[^0-9a-f]*'") {
		t.Error("migration 0013's fingerprint trigger no longer matches the application regex")
	}
}

// A ring cannot be larger than the directory that authorizes it: every rank must
// be a peer the coordinator listed, and it lists at most MAX_PEERS of them.
func TestRingRankBoundFitsTheCoordinatorPeerLimit(t *testing.T) {
	src := platformFile(t, "apps/coordinator/src/peers.ts")
	m := regexp.MustCompile(`export const MAX_PEERS = (\d+);`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("MAX_PEERS not found in peers.ts")
	}
	limit, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	// MaxRingRanks counts this host too, so the directory need only cover the
	// other ranks; requiring the full count is the stricter, safer comparison.
	if MaxRingRanks > limit {
		t.Errorf("MaxRingRanks = %d but the coordinator lists at most %d peers", MaxRingRanks, limit)
	}
}
