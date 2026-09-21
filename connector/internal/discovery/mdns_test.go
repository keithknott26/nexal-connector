package discovery

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

const (
	fpSelf  = "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"
	fpPeer  = "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"
	fpThird = "cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333"
)

func sampleAdvertisement() Advertisement {
	return Advertisement{
		HostID: "mac1", Fingerprint: fpSelf, Version: "0.1.0", Port: 8443,
		Addresses: []netip.Addr{netip.MustParseAddr("192.168.4.9")},
	}
}

func peerAdvertisement() Advertisement {
	return Advertisement{
		HostID: "mac2", Fingerprint: fpPeer, Version: "0.1.0", Port: 8443,
		Addresses: []netip.Addr{netip.MustParseAddr("192.168.4.7")},
	}
}

// responsePacket encodes an advertisement the way a responder would send it.
func responsePacket(t testing.TB, ad Advertisement) []byte {
	t.Helper()
	answers, extra, err := ad.records()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := encodeMessage(&message{flags: flagResponse, answers: answers, extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func queryPacket(t testing.TB) []byte {
	t.Helper()
	payload, err := QueryPacket()
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestAdvertisementValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Advertisement)
	}{
		{"empty host id", func(a *Advertisement) { a.HostID = "" }},
		{"host id is not one dns label", func(a *Advertisement) { a.HostID = "mac.one" }},
		{"host id longer than a label", func(a *Advertisement) { a.HostID = strings.Repeat("h", 64) }},
		{"uppercase fingerprint", func(a *Advertisement) { a.Fingerprint = strings.ToUpper(fpSelf) }},
		{"short fingerprint", func(a *Advertisement) { a.Fingerprint = fpSelf[:63] }},
		{"non-hex fingerprint", func(a *Advertisement) { a.Fingerprint = strings.Repeat("g", 64) }},
		{"zero port", func(a *Advertisement) { a.Port = 0 }},
		{"public address", func(a *Advertisement) { a.Addresses = []netip.Addr{netip.MustParseAddr("93.184.216.34")} }},
		{"version with a control character", func(a *Advertisement) { a.Version = "0.1.0\n" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ad := sampleAdvertisement()
			tc.edit(&ad)
			if err := ad.validate(); err == nil {
				t.Fatal("accepted an invalid advertisement")
			}
			if _, err := NewResponder(ad, nil); err == nil {
				t.Fatal("built a responder from an invalid advertisement")
			}
		})
	}
}

func TestAdvertisementTXTCarriesTheContractKeys(t *testing.T) {
	txt := sampleAdvertisement().txt()
	want := []string{"fp=" + fpSelf, "v=0.1.0", "h=mac1"}
	if len(txt) != len(want) {
		t.Fatalf("TXT is %v", txt)
	}
	for i := range want {
		if txt[i] != want[i] {
			t.Fatalf("TXT[%d] = %q, want %q", i, txt[i], want[i])
		}
	}
}

func TestParseCandidatesRoundTripsAnAdvertisement(t *testing.T) {
	from := netip.MustParseAddrPort("192.168.4.7:5353")
	now := time.Unix(1700000000, 0)
	candidates, err := ParseCandidates(responsePacket(t, peerAdvertisement()), from, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("%d candidates", len(candidates))
	}
	c := candidates[0]
	if c.HostID != "mac2" || c.Fingerprint != fpPeer || c.Port != 8443 || c.Version != "0.1.0" {
		t.Fatalf("candidate %+v", c)
	}
	if c.From != from.Addr() || !c.SeenAt.Equal(now) {
		t.Fatalf("candidate provenance %+v", c)
	}
	if len(c.Addresses) != 1 || c.Addresses[0] != netip.MustParseAddr("192.168.4.7") {
		t.Fatalf("candidate addresses %v", c.Addresses)
	}
}

// TestParseCandidatesDropsUnusableClaims covers the claims a hostile or broken
// responder can make. Every one of them is dropped rather than repaired: a
// half-understood claim is not a peer.
func TestParseCandidatesDropsUnusableClaims(t *testing.T) {
	from := netip.MustParseAddrPort("192.168.4.7:5353")
	build := func(t *testing.T, txt []string, port uint16, instance string) []byte {
		t.Helper()
		answers := []record{
			{name: ServiceName, rtype: typePTR, class: classIN, ttl: 60, ptr: instance},
			{name: instance, rtype: typeSRV, class: classIN, ttl: 60, port: port, target: "mac2.local."},
		}
		if len(txt) > 0 {
			answers = append(answers, record{name: instance, rtype: typeTXT, class: classIN, ttl: 60, txt: txt})
		}
		payload, err := encodeMessage(&message{flags: flagResponse, answers: answers})
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	tests := []struct {
		name     string
		txt      []string
		port     uint16
		instance string
	}{
		{"no TXT at all", nil, 8443, "mac2." + ServiceName},
		{"no fingerprint", []string{"v=0.1.0", "h=mac2"}, 8443, "mac2." + ServiceName},
		{"short fingerprint", []string{"fp=" + fpPeer[:60], "h=mac2"}, 8443, "mac2." + ServiceName},
		{"uppercase fingerprint", []string{"fp=" + strings.ToUpper(fpPeer), "h=mac2"}, 8443, "mac2." + ServiceName},
		{"zero port", []string{"fp=" + fpPeer, "h=mac2"}, 0, "mac2." + ServiceName},
		{"host id disagrees with instance", []string{"fp=" + fpPeer, "h=mac9"}, 8443, "mac2." + ServiceName},
		{"host id is not a label", []string{"fp=" + fpPeer, "h=mac 2"}, 8443, "mac2." + ServiceName},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseCandidates(build(t, tc.txt, tc.port, tc.instance), from, time.Unix(1, 0)); err == nil {
				t.Fatalf("accepted %d candidates from an unusable claim: %+v", len(got), got)
			}
		})
	}
	// A query is not an announcement, whatever it contains.
	if _, err := ParseCandidates(queryPacket(t), from, time.Unix(1, 0)); err == nil {
		t.Fatal("extracted candidates from a query")
	}
}

func TestResponderAnswersOnlyItsOwnService(t *testing.T) {
	clock := func() time.Time { return time.Unix(1700000000, 0) }
	r, err := NewResponder(sampleAdvertisement(), clock)
	if err != nil {
		t.Fatal(err)
	}
	from := netip.MustParseAddrPort("192.168.4.7:5353")
	reply, err := r.Respond(queryPacket(t), from)
	if err != nil {
		t.Fatalf("refused a legitimate browse query: %v", err)
	}
	candidates, err := ParseCandidates(reply, netip.MustParseAddrPort("192.168.4.9:5353"), clock())
	if err != nil || len(candidates) != 1 || candidates[0].Fingerprint != fpSelf {
		t.Fatalf("reply did not carry this host's advertisement: %v %+v", err, candidates)
	}

	other, err := encodeMessage(&message{questions: []question{
		{name: "_airplay._tcp.local.", qtype: typePTR, class: classIN},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Respond(other, from); err == nil {
		t.Fatal("answered a query for somebody else's service")
	}
	if _, err := r.Respond(responsePacket(t, peerAdvertisement()), from); err == nil {
		t.Fatal("answered a response")
	}
	if _, err := r.Respond(queryPacket(t), netip.MustParseAddrPort("93.184.216.34:5353")); err == nil {
		t.Fatal("answered a non-local source address")
	}
}

// TestResponderNeverAmplifies pins the two properties that keep a discovery
// responder off a reflection-attack list: the reply is a function of our own
// configuration and not of the request, and one query earns at most one reply.
func TestResponderNeverAmplifies(t *testing.T) {
	now := time.Unix(1700000000, 0)
	r, err := NewResponder(sampleAdvertisement(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	from := netip.MustParseAddrPort("192.168.4.7:5353")
	small, err := r.Respond(queryPacket(t), from)
	if err != nil {
		t.Fatal(err)
	}
	// The same question asked maxQuestions times must not grow the reply.
	questions := make([]question, maxQuestions)
	for i := range questions {
		questions[i] = question{name: ServiceName, qtype: typePTR, class: classIN}
	}
	stuffed, err := encodeMessage(&message{questions: questions})
	if err != nil {
		t.Fatal(err)
	}
	big, err := r.Respond(stuffed, from)
	if err != nil {
		t.Fatal(err)
	}
	if len(big) != len(small) {
		t.Fatalf("reply size depends on the request: %d vs %d bytes", len(big), len(small))
	}
	if len(stuffed) < len(big) {
		t.Logf("reply %d bytes for a %d byte request: ratio %.2f", len(big), len(stuffed), float64(len(big))/float64(len(stuffed)))
	}
}

func TestResponderRateLimits(t *testing.T) {
	now := time.Unix(1700000000, 0)
	r, err := NewResponder(sampleAdvertisement(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	query := queryPacket(t)
	from := netip.MustParseAddrPort("192.168.4.7:5353")
	var answered int
	for range 20 {
		if _, err := r.Respond(query, from); err == nil {
			answered++
		}
	}
	if answered == 0 || answered > 3 {
		t.Fatalf("answered %d of 20 identical queries from one source", answered)
	}
	// A flood from many sources is bounded by the global limiter too.
	answered = 0
	for i := range 100 {
		source := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i / 256), byte(i % 256)}), 5353)
		if _, err := r.Respond(query, source); err == nil {
			answered++
		}
	}
	if answered > 10 {
		t.Fatalf("global limiter let %d replies through at a fixed instant", answered)
	}
	// Tokens come back with time, not with pressure.
	now = now.Add(time.Minute)
	if _, err := r.Respond(query, from); err != nil {
		t.Fatalf("limiter did not refill: %v", err)
	}
}

func TestBucketIgnoresBackwardClock(t *testing.T) {
	start := time.Unix(1700000000, 0)
	b := bucket{capacity: 2, refill: time.Second, tokens: 2, updated: start}
	if !b.take(start) || !b.take(start) || b.take(start) {
		t.Fatal("bucket did not spend exactly its capacity")
	}
	if b.take(start.Add(-time.Hour)) {
		t.Fatal("a clock that moved backwards minted a token")
	}
}

func TestListenIsGatedOffByDefault(t *testing.T) {
	if _, err := Listen(Options{Advertisement: sampleAdvertisement(), Sharing: DefaultSharing()}); err != ErrSharingDisabled {
		t.Fatalf("Listen with the default policy returned %v, want ErrSharingDisabled", err)
	}
}

func TestMulticastGroupsMatchTheContract(t *testing.T) {
	if MulticastIPv4.String() != "224.0.0.251:5353" {
		t.Fatalf("IPv4 group is %s", MulticastIPv4)
	}
	if MulticastIPv6.String() != "[ff02::fb]:5353" {
		t.Fatalf("IPv6 group is %s", MulticastIPv6)
	}
	if ServiceName != "_nexal._tcp.local." {
		t.Fatalf("service name is %s", ServiceName)
	}
}
