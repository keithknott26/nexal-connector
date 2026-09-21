package discovery

import (
	"net/netip"
	"testing"
	"time"
)

func lanView() LinkView {
	return NewLinkView([]netip.Prefix{
		netip.MustParsePrefix("192.168.4.0/24"),
		netip.MustParsePrefix("fd00:1::/64"),
	})
}

func authorizedSnapshot(peers ...AuthorizedPeer) Snapshot {
	return Snapshot{Peers: peers, ObservedAt: time.Unix(1700000000, 0)}
}

// TestMDNSOnlyPeerNeverEntersAllowedPeers is the test the contract demands
// directly. A machine that exists only as a multicast claim must not reach the
// mutual-TLS fingerprint allowlist, no matter how complete or how local its
// advertisement looks.
func TestMDNSOnlyPeerNeverEntersAllowedPeers(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// The hostile case, spelled out: a stranger on the same Wi-Fi advertises a
	// well-formed Nexal record, from an address inside our own prefix, with a
	// perfectly valid fingerprint. Everything about it is shaped like a peer.
	stranger := Candidate{
		Instance: "mac9." + ServiceName, HostID: "mac9", Fingerprint: fpThird,
		Version: "0.1.0", Port: 8443,
		From:      netip.MustParseAddr("192.168.4.31"),
		Addresses: []netip.Addr{netip.MustParseAddr("192.168.4.31")},
		SeenAt:    now,
	}
	authorized := AuthorizedPeer{
		HostID: "mac2", Name: "Studio", Fingerprint: fpPeer,
		Addresses: []AddressClaim{{Kind: "lan", Address: "192.168.4.7", Port: 8443}},
	}
	peers := Merge(authorizedSnapshot(authorized), []Candidate{stranger}, lanView(), now)

	allowed := AllowedPeers(peers)
	if len(allowed) != 1 || allowed[0] != fpPeer {
		t.Fatalf("AllowedPeers = %v, want exactly the coordinator-authorized fingerprint", allowed)
	}
	for _, p := range peers {
		if p.Fingerprint == fpThird && p.Authorized {
			t.Fatal("an mDNS-only peer was marked authorized")
		}
	}
	// And with no coordinator list at all, a LAN full of candidates authorizes
	// nobody: an unreachable control plane fails closed, it does not fail open.
	empty := Merge(Snapshot{}, []Candidate{stranger}, lanView(), now)
	if got := AllowedPeers(empty); len(got) != 0 {
		t.Fatalf("AllowedPeers = %v with no coordinator list, want empty", got)
	}
	if len(Unauthorized(empty)) != 1 {
		t.Fatal("an unauthorized candidate should still be displayable as found-not-authorized")
	}
	// A candidate impersonating an authorized peer's fingerprint cannot widen
	// anything either: the set is already exactly one entry, and the peer TLS
	// still has to see that key in the handshake.
	impersonator := stranger
	impersonator.Fingerprint = fpPeer
	impersonator.HostID = "mac2"
	impersonator.Instance = "mac2." + ServiceName
	if got := AllowedPeers(Merge(authorizedSnapshot(authorized), []Candidate{impersonator}, lanView(), now)); len(got) != 1 || got[0] != fpPeer {
		t.Fatalf("AllowedPeers = %v after an impersonation attempt", got)
	}
}

// TestAllowedPeersIgnoresEveryLANInfluencedField guards the derivation against a
// future edit: no field a multicast packet can set may change the result.
func TestAllowedPeersIgnoresEveryLANInfluencedField(t *testing.T) {
	base := Peer{HostID: "mac2", Fingerprint: fpPeer, Authorized: false}
	variants := []Peer{base, base, base, base}
	variants[0].Source = SourceMDNS
	variants[1].Source = SourceBoth
	variants[2].Locality = LocalitySameLink
	variants[3].SeenOnLAN = true
	for i, p := range variants {
		if got := AllowedPeers([]Peer{p}); len(got) != 0 {
			t.Fatalf("variant %d produced %v", i, got)
		}
	}
	if got := AllowedPeers([]Peer{{HostID: "mac2", Fingerprint: fpPeer, Authorized: true}}); len(got) != 1 {
		t.Fatalf("an authorized peer was dropped: %v", got)
	}
	// A malformed fingerprint is never allowlisted even if the coordinator says
	// authorized: pool.peerPolicy would reject it anyway, and passing it through
	// would turn a bad row into a construction error at startup.
	if got := AllowedPeers([]Peer{{HostID: "mac2", Fingerprint: "nope", Authorized: true}}); len(got) != 0 {
		t.Fatalf("allowlisted an invalid fingerprint: %v", got)
	}
}

func TestMergeMarksSourceAndKeepsAuthorizationFromTheCoordinator(t *testing.T) {
	now := time.Unix(1700000000, 0)
	authorized := []AuthorizedPeer{
		{HostID: "mac2", Name: "Studio", Fingerprint: fpPeer,
			Addresses: []AddressClaim{{Kind: "lan", Address: "192.168.4.7", Port: 8443}}},
		{HostID: "mac3", Name: "Remote", Fingerprint: fpSelf,
			Addresses: []AddressClaim{{Kind: "wan", Address: "93.184.216.34", Port: 8443}}},
	}
	seen := []Candidate{{
		Instance: "mac2." + ServiceName, HostID: "mac2", Fingerprint: fpPeer, Port: 8443,
		From: netip.MustParseAddr("192.168.4.7"), SeenAt: now, Version: "0.1.0",
	}}
	peers := Merge(authorizedSnapshot(authorized...), seen, lanView(), now)
	if len(peers) != 2 {
		t.Fatalf("%d peers", len(peers))
	}
	byID := map[string]Peer{}
	for _, p := range peers {
		byID[p.HostID] = p
	}
	if got := byID["mac2"]; got.Source != SourceBoth || !got.Authorized || got.Locality != LocalitySameLink || !got.SeenOnLAN {
		t.Fatalf("mac2 merged as %+v", got)
	}
	if got := byID["mac3"]; got.Source != SourceRendezvous || !got.Authorized || got.Locality != LocalityWAN {
		t.Fatalf("mac3 merged as %+v", got)
	}
	if byID["mac2"].SourceLabel() != "both" || byID["mac3"].LocalityLabel() != "wan" {
		t.Fatal("string labels do not match the enums")
	}
	// Staleness travels with the merged rows so a UI can say the list is old.
	stale := authorizedSnapshot(authorized...)
	stale.Stale = true
	for _, p := range Merge(stale, nil, lanView(), now) {
		if !p.AuthorizationStale {
			t.Fatal("stale authorization was not propagated to the merged peer")
		}
	}
}

// TestLocalityIsConservative encodes the two failure modes §34.3 and §35.5 warn
// about: a shared /24 is not the test, and a VPN address must not read as
// adjacent.
func TestLocalityIsConservative(t *testing.T) {
	// The host is on 192.168.4.0/24 and holds a corporate VPN address on a
	// point-to-point interface, which LocalLinkView excludes — so the VPN's
	// 10.x prefix is not in the view at all.
	view := lanView()
	tests := []struct {
		name     string
		addr     string
		mdns     bool
		expected Locality
	}{
		{"same prefix, heard over multicast", "192.168.4.7", true, LocalitySameLink},
		{"same prefix, coordinator only", "192.168.4.7", false, LocalityLAN},
		{"vpn 10.x that shares no prefix with us", "10.8.0.4", false, LocalityUnknown},
		{"another site's rfc1918", "192.168.9.4", false, LocalityUnknown},
		{"same /24 in a different family of prefix, not attached", "172.16.4.7", false, LocalityUnknown},
		{"cgnat", "100.64.3.9", false, LocalityUnknown},
		{"public address", "93.184.216.34", false, LocalityWAN},
		{"link-local without multicast evidence", "169.254.9.9", false, LocalityUnknown},
		{"link-local heard over multicast", "169.254.9.9", true, LocalitySameLink},
		{"ipv6 ula in our prefix over multicast", "fd00:1::5", true, LocalitySameLink},
		{"ipv6 ula outside our prefix", "fd00:9::5", false, LocalityUnknown},
		{"loopback", "127.0.0.1", true, LocalityUnknown},
		{"multicast", "224.0.0.251", true, LocalityUnknown},
		{"unspecified", "0.0.0.0", true, LocalityUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := view.Classify(netip.MustParseAddr(tc.addr), tc.mdns); got != tc.expected {
				t.Fatalf("Classify(%s, mdns=%v) = %s, want %s", tc.addr, tc.mdns, got, tc.expected)
			}
		})
	}
	// An empty view — no interface information at all — must never guess
	// SameLink for a private address.
	if got := (LinkView{}).Classify(netip.MustParseAddr("192.168.4.7"), false); got != LocalityUnknown {
		t.Fatalf("empty view classified a private address as %s", got)
	}
	// Multicast evidence alone, on a prefix we do not hold, is still not
	// SameLink: the record's address may not be the address we heard from.
	if got := (LinkView{}).Classify(netip.MustParseAddr("192.168.4.7"), true); got != LocalityUnknown {
		t.Fatalf("empty view with multicast evidence classified as %s", got)
	}
	if got := view.Classify(netip.Addr{}, true); got != LocalityUnknown {
		t.Fatalf("invalid address classified as %s", got)
	}
}

func TestLocalityGrantsNothing(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// A same-link, unauthorized peer and a WAN, authorized peer. Locality must
	// not reorder authorization.
	seen := []Candidate{{
		Instance: "mac9." + ServiceName, HostID: "mac9", Fingerprint: fpThird, Port: 8443,
		From: netip.MustParseAddr("192.168.4.31"), SeenAt: now,
	}}
	authorized := AuthorizedPeer{HostID: "mac3", Fingerprint: fpSelf,
		Addresses: []AddressClaim{{Kind: "wan", Address: "93.184.216.34", Port: 8443}}}
	peers := Merge(authorizedSnapshot(authorized), seen, lanView(), now)
	if !peers[0].Authorized || peers[0].Locality != LocalityWAN {
		t.Fatalf("authorized WAN peer did not sort first: %+v", peers)
	}
	if got := AllowedPeers(peers); len(got) != 1 || got[0] != fpSelf {
		t.Fatalf("AllowedPeers = %v", got)
	}
}

func TestMergeBoundsAndSanitizesItsInput(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// Rows the coordinator should never send, but which must not become peers if
	// it does.
	bad := Snapshot{Peers: []AuthorizedPeer{
		{HostID: "mac2", Fingerprint: "short"},
		{HostID: "", Fingerprint: fpPeer},
		{HostID: "has space", Fingerprint: fpPeer},
	}, ObservedAt: now}
	if peers := Merge(bad, nil, lanView(), now); len(peers) != 0 {
		t.Fatalf("merged unusable coordinator rows: %+v", peers)
	}
	// A candidate flood cannot make one peer carry unbounded addresses.
	var flood []Candidate
	for i := range 40 {
		flood = append(flood, Candidate{
			Instance: "mac2." + ServiceName, HostID: "mac2", Fingerprint: fpPeer, Port: 8443,
			From:   netip.AddrFrom4([4]byte{192, 168, 4, byte(i + 1)}),
			SeenAt: now,
		})
	}
	peers := Merge(Snapshot{}, flood, lanView(), now)
	if len(peers) != 1 {
		t.Fatalf("%d peers from a flood of one fingerprint", len(peers))
	}
	if len(peers[0].Addresses) > 16 {
		t.Fatalf("%d addresses retained", len(peers[0].Addresses))
	}
}

func TestSharingDefaultsAreOff(t *testing.T) {
	// §36.4 is an unresolved founder decision. The mechanism exists; the default
	// is off and it is not decided in code.
	def := DefaultSharing()
	if def.LANDiscovery || def.WANRendezvous || def.ResourceSharing {
		t.Fatalf("default sharing policy is not closed: %+v", def)
	}
	var zero SharingPolicy
	if zero != def {
		t.Fatal("the zero value of SharingPolicy is not the closed state")
	}
}

func TestValidatorsMatchTheIdentityContract(t *testing.T) {
	if !ValidFingerprint(fpPeer) {
		t.Fatal("rejected a valid device fingerprint")
	}
	for _, bad := range []string{"", fpPeer[:63], fpPeer + "a", "AAAA1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
		"gggg1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"} {
		if ValidFingerprint(bad) {
			t.Fatalf("accepted %q as a fingerprint", bad)
		}
	}
	for _, bad := range []string{"", "mac 1", "mac.1", "mac/1", "über"} {
		if ValidHostID(bad) {
			t.Fatalf("accepted %q as a host id", bad)
		}
	}
}

func TestLocalLinkViewDoesNotFail(t *testing.T) {
	// The real interface read must not error on this machine and must never
	// return a prefix that would classify a public address as local.
	view, err := LocalLinkView()
	if err != nil {
		t.Skipf("interface enumeration unavailable: %v", err)
	}
	if view.Classify(netip.MustParseAddr("93.184.216.34"), false) != LocalityWAN {
		t.Fatal("a public address was not classified as WAN against real interfaces")
	}
}
