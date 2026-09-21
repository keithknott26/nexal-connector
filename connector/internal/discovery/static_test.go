package discovery

import (
	"net/netip"
	"testing"
	"time"
)

const (
	staticFP     = "aa11223344556677889900112233445566778899001122334455667788990011"
	authorizedFP = "bb11223344556677889900112233445566778899001122334455667788990011"
)

// THE TEST THAT MATTERS. A static peer is an owner-typed address, and an address
// is not membership (HARDENING-PLAN §30.2). If this ever passes with the
// fingerprint present in AllowedPeers, editing a config file has become an
// authorization mechanism.
func TestStaticPeerNeverEntersAllowedPeers(t *testing.T) {
	static := []Static{{Fingerprint: staticFP, Address: netip.MustParseAddrPort("10.20.0.5:8443")}}
	peers := MergeStatic(Snapshot{}, nil, static, NewLinkView(nil), time.Now())
	if len(peers) != 1 {
		t.Fatalf("peers = %+v", peers)
	}
	if peers[0].Authorized {
		t.Fatal("a configured peer must never be authorized by its configuration")
	}
	if !peers[0].Configured || peers[0].Source != SourceStatic || peers[0].SourceLabel() != "static" {
		t.Fatalf("peer = %+v; a configured peer must be labelled as configured, not discovered", peers[0])
	}
	if allowed := AllowedPeers(peers); len(allowed) != 0 {
		t.Fatalf("AllowedPeers = %v, want empty", allowed)
	}
	if un := Unauthorized(peers); len(un) != 1 {
		t.Fatal("a configured, unauthorized peer must be visible as such")
	}
}

// When the coordinator DOES authorize the fingerprint, the static entry
// contributes its address and takes dial preference, because the owner named it
// precisely because discovery cannot reach that peer.
func TestStaticAddressIsPreferredForAnAuthorizedPeer(t *testing.T) {
	snapshot := Snapshot{Peers: []AuthorizedPeer{{HostID: "studio", Fingerprint: authorizedFP,
		Addresses: []AddressClaim{{Address: "192.168.9.9", Port: 8443}}}}}
	static := []Static{{Fingerprint: authorizedFP, Address: netip.MustParseAddrPort("10.20.0.5:8443"), Label: "vlan 20"}}
	peers := MergeStatic(snapshot, nil, static, NewLinkView(nil), time.Now())
	if len(peers) != 1 || !peers[0].Authorized || !peers[0].Configured {
		t.Fatalf("peers = %+v", peers)
	}
	if got := peers[0].Addresses[0].String(); got != "10.20.0.5:8443" {
		t.Fatalf("first dial candidate = %s, want the configured address", got)
	}
	if len(peers[0].Addresses) != 2 {
		t.Fatalf("the directory address must be kept as a fallback: %v", peers[0].Addresses)
	}
	// Authorization still came from the coordinator alone, which is what makes
	// this fingerprint dialable.
	if allowed := AllowedPeers(peers); len(allowed) != 1 || allowed[0] != authorizedFP {
		t.Fatalf("allowed = %v", allowed)
	}
}

func TestMergeStaticIgnoresMalformedEntries(t *testing.T) {
	static := []Static{
		{Fingerprint: "short", Address: netip.MustParseAddrPort("10.20.0.5:8443")},
		{Fingerprint: staticFP},                            // no address
		{Fingerprint: staticFP, Address: netip.AddrPort{}}, // invalid address
	}
	if peers := MergeStatic(Snapshot{}, nil, static, NewLinkView(nil), time.Now()); len(peers) != 0 {
		t.Fatalf("peers = %+v, want none", peers)
	}
}

// MergeStatic with no static entries must behave exactly like Merge, so the
// existing behaviour is untouched for a config with no static peers.
func TestMergeStaticWithoutStaticPeersEqualsMerge(t *testing.T) {
	now := time.Now()
	snapshot := Snapshot{Peers: []AuthorizedPeer{{HostID: "studio", Fingerprint: authorizedFP}}}
	candidates := []Candidate{{HostID: "mini", Fingerprint: staticFP, Port: 8443,
		From: netip.MustParseAddr("192.168.1.9"), SeenAt: now}}
	a := Merge(snapshot, candidates, NewLinkView(nil), now)
	b := MergeStatic(snapshot, candidates, nil, NewLinkView(nil), now)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Fingerprint != b[i].Fingerprint || a[i].Authorized != b[i].Authorized ||
			b[i].Configured || len(a[i].Addresses) != len(b[i].Addresses) {
			t.Fatalf("row %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestPrependAddressBoundsAndDeduplicates(t *testing.T) {
	addr := netip.MustParseAddrPort("10.20.0.5:8443")
	list := []netip.AddrPort{netip.MustParseAddrPort("192.168.1.1:8443"), addr}
	got := prependAddress(list, addr)
	if len(got) != 2 || got[0] != addr {
		t.Fatalf("got %v; an existing address must move to the front, not duplicate", got)
	}
	full := make([]netip.AddrPort, 16)
	for i := range full {
		full[i] = netip.AddrPortFrom(netip.MustParseAddr("192.168.1.1"), uint16(1000+i))
	}
	got = prependAddress(full, addr)
	if len(got) != 16 || got[0] != addr {
		t.Fatalf("got %d addresses; the list must stay bounded with the configured address kept", len(got))
	}
}
