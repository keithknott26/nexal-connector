package agent

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/discovery"
)

const (
	selfFingerprint        = "1111111111111111111111111111111111111111111111111111111111111111"
	coordinatorFingerprint = "2222222222222222222222222222222222222222222222222222222222222222"
	mdnsOnlyFingerprint    = "3333333333333333333333333333333333333333333333333333333333333333"
)

// fakePublisher records every advertise, so a test can assert that the agent
// publishes on start and republishes on its interval.
type fakePublisher struct {
	mu    sync.Mutex
	calls []client.Advertisement
	err   error
}

func (f *fakePublisher) Advertise(_ context.Context, fingerprint string,
	addresses []client.LANAddress, capabilities *client.PeerCapabilities) (client.AdvertiseAck, error) {
	f.mu.Lock()
	f.calls = append(f.calls, client.Advertisement{Fingerprint: fingerprint,
		Addresses: addresses, Capabilities: capabilities})
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return client.AdvertiseAck{}, err
	}
	return client.AdvertiseAck{OK: true, Fingerprint: fingerprint,
		LANAddresses: len(addresses), ObservedWANAddress: "203.0.113.7",
		Authorization:      client.AuthorizationCandidateList,
		CapabilityEvidence: client.CapabilityEvidenceSelfReported,
		IdentityEvidence:   client.IdentityEvidenceSelfReported}, nil
}

func (f *fakePublisher) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }
func (f *fakePublisher) last() (client.Advertisement, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return client.Advertisement{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// fakeDirectory is the coordinator's peer list read.
type fakeDirectory struct {
	mu        sync.Mutex
	directory client.PeerDirectory
	err       error
	reads     int
}

func (f *fakeDirectory) Peers(context.Context) (client.PeerDirectory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.directory, f.err
}
func (f *fakeDirectory) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.reads }

func oneAuthorizedPeer() client.PeerDirectory {
	return client.PeerDirectory{
		Peers: []client.DirectoryPeer{{HostID: "h2", Name: "studio",
			Fingerprint: coordinatorFingerprint,
			Addresses: []client.PeerAddress{{Kind: "lan", Address: "192.168.1.10", Port: 7443,
				ObservedAt: "2026-09-20T23:00:00.000Z"}},
			LastSeenAt: "2026-09-20T23:00:00.000Z"}},
		Authorization:      client.AuthorizationCandidateList,
		CapabilityEvidence: client.CapabilityEvidenceSelfReported,
		IdentityEvidence:   client.IdentityEvidenceSelfReported,
		PeerLimit:          200, FreshnessSeconds: 900,
	}
}

// discoveryAgent builds an agent with discovery wired to fakes and short
// intervals. The LAN gate stays shut: joining real multicast groups is not
// something a unit test should depend on, and the LAN path is exercised through
// the candidate set directly below.
func discoveryAgent(t *testing.T, o DiscoveryOptions) *Agent {
	t.Helper()
	a, _ := testAgent(t)
	if o.Config == nil {
		o.Config = &config.Discovery{WANRendezvous: true,
			DeviceFingerprint: selfFingerprint, PeerPort: 7443}
	}
	if o.Sharing == (discovery.SharingPolicy{}) {
		o.Sharing = discovery.SharingPolicy{WANRendezvous: true}
	}
	if o.LocalAddresses == nil {
		o.LocalAddresses = func() []netip.Addr {
			return []netip.Addr{netip.MustParseAddr("192.168.1.22")}
		}
	}
	if o.AdvertiseInterval == 0 {
		o.AdvertiseInterval = 10 * time.Millisecond
	}
	if o.MergeInterval == 0 {
		o.MergeInterval = 5 * time.Millisecond
	}
	WithDiscovery(o)(a)
	return a
}

// waitFor polls until cond or the deadline, so the loops can be observed without
// sleeping for a fixed interval.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole point of the wiring: the agent publishes its addresses on start, so
// the coordinator's host_addresses row for this host is not empty, and it keeps
// republishing so an address change reaches peers.
func TestAgentAdvertisesOnStartAndRepublishes(t *testing.T) {
	publisher := &fakePublisher{}
	directory := &fakeDirectory{directory: oneAuthorizedPeer()}
	a := discoveryAgent(t, DiscoveryOptions{Publisher: publisher, Source: directory,
		Capabilities: &client.PeerCapabilities{Chip: "M4 Max", OSVersion: "26.2"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); a.runDiscovery(ctx) }()

	waitFor(t, "the first advertise", func() bool { return publisher.count() >= 1 })
	waitFor(t, "a republish", func() bool { return publisher.count() >= 2 })
	waitFor(t, "a peer directory read", func() bool { return directory.count() >= 1 })

	sent, _ := publisher.last()
	if sent.Fingerprint != selfFingerprint {
		t.Errorf("advertised fingerprint = %q", sent.Fingerprint)
	}
	if len(sent.Addresses) != 1 || sent.Addresses[0] !=
		(client.LANAddress{Kind: "lan", Address: "192.168.1.22", Port: 7443}) {
		t.Errorf("advertised addresses = %+v", sent.Addresses)
	}
	if sent.Capabilities == nil || sent.Capabilities.Chip != "M4 Max" {
		t.Errorf("self-reported capabilities were not published: %+v", sent.Capabilities)
	}
	waitFor(t, "the merged view", func() bool { return len(a.PeerCandidates().Allowed) == 1 })
	view := a.PeerCandidates()
	if view.Allowed[0] != coordinatorFingerprint {
		t.Errorf("allowed = %v", view.Allowed)
	}
	if view.LastAdvertisedAt.IsZero() || view.ObservedWANAddress != "203.0.113.7" {
		t.Errorf("advertise result not recorded: %+v", view)
	}
	cancel()
	<-done
}

// THE invariant. An mDNS candidate the coordinator did not list is displayable
// and never authorized, so it must never reach AllowedPeers — the agent-level
// counterpart of discovery.TestMDNSOnlyPeerNeverEntersAllowedPeers, because the
// wiring is where an accidental shortcut would be introduced.
func TestAgentMDNSOnlyPeerNeverEntersAllowedPeers(t *testing.T) {
	directory := &fakeDirectory{directory: oneAuthorizedPeer()}
	a := discoveryAgent(t, DiscoveryOptions{Publisher: &fakePublisher{}, Source: directory})
	rendezvous, err := discovery.NewRendezvous(discovery.RendezvousOptions{
		Source: directory, Sharing: discovery.SharingPolicy{WANRendezvous: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rendezvous.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	lan := newCandidateSet(time.Now)
	lan.add([]discovery.Candidate{
		// A hostile LAN claim: valid shape, never listed by the coordinator.
		{HostID: "h9", Fingerprint: mdnsOnlyFingerprint, Port: 7443, SeenAt: now,
			From: netip.MustParseAddr("192.168.1.99")},
		// And a claim about a peer the coordinator did list, which may contribute
		// addresses and locality but no new authorization.
		{HostID: "h2", Fingerprint: coordinatorFingerprint, Port: 7443, SeenAt: now,
			From: netip.MustParseAddr("192.168.1.10")},
	})
	a.mergePeers(rendezvous, lan, discovery.NewLinkView(nil), now)
	view := a.PeerCandidates()
	if len(view.Allowed) != 1 || view.Allowed[0] != coordinatorFingerprint {
		t.Fatalf("allowed = %v, want only the coordinator's peer", view.Allowed)
	}
	var seenUnauthorized bool
	for _, p := range view.Peers {
		if p.Fingerprint == mdnsOnlyFingerprint {
			seenUnauthorized = true
			if p.Authorized {
				t.Fatal("an mDNS-only peer was marked authorized")
			}
		}
	}
	if !seenUnauthorized {
		t.Error("an mDNS-only peer should still be displayable as found, not authorized")
	}
}

// A closed gate is the default, and the default must do nothing at all: no
// advertise, no directory read, no socket.
func TestAgentDiscoveryDoesNothingWhenGatesAreShut(t *testing.T) {
	publisher := &fakePublisher{}
	directory := &fakeDirectory{directory: oneAuthorizedPeer()}
	a, _ := testAgent(t)
	WithDiscovery(DiscoveryOptions{
		Config:  &config.Discovery{DeviceFingerprint: selfFingerprint, PeerPort: 7443},
		Sharing: discovery.SharingPolicy{}, Publisher: publisher, Source: directory,
		AdvertiseInterval: time.Millisecond, MergeInterval: time.Millisecond,
	})(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.runDiscovery(ctx) // returns immediately; a shut gate is not an error.
	time.Sleep(20 * time.Millisecond)
	if publisher.count() != 0 || directory.count() != 0 {
		t.Fatalf("a shut gate still talked to the coordinator: %d advertises, %d reads",
			publisher.count(), directory.count())
	}
	if len(a.PeerCandidates().Allowed) != 0 {
		t.Fatal("a shut gate produced an allowlist")
	}
}

// An agent with no discovery option behaves exactly as before: Run must not
// start anything, and must not panic on the nil options.
func TestAgentWithoutDiscoveryOptionRunsNothing(t *testing.T) {
	a, _ := testAgent(t)
	a.runDiscovery(context.Background())
	if a.PeerCandidates().Peers != nil {
		t.Fatal("discovery ran without being configured")
	}
}

// A gate opened without a fingerprint cannot publish an identity, so it fails
// closed instead of advertising something invented. config.Discovery.Validate
// refuses this earlier; this is the belt to that braces.
func TestAgentDiscoveryFailsClosedWithoutFingerprint(t *testing.T) {
	publisher := &fakePublisher{}
	a, _ := testAgent(t)
	WithDiscovery(DiscoveryOptions{
		Config:  &config.Discovery{WANRendezvous: true, PeerPort: 7443},
		Sharing: discovery.SharingPolicy{WANRendezvous: true}, Publisher: publisher,
		Source: &fakeDirectory{}, AdvertiseInterval: time.Millisecond,
	})(a)
	a.runDiscovery(context.Background())
	time.Sleep(20 * time.Millisecond)
	if publisher.count() != 0 {
		t.Fatal("advertised without a device fingerprint")
	}
}

// A failed advertise is recorded and retried, and the previous view is not
// destroyed: an unreachable coordinator must never widen or empty anything.
func TestAgentAdvertiseFailureIsRecordedAndRetried(t *testing.T) {
	publisher := &fakePublisher{err: errors.New("coordinator unreachable")}
	a := discoveryAgent(t, DiscoveryOptions{Publisher: publisher,
		Source: &fakeDirectory{directory: oneAuthorizedPeer()}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); a.runDiscovery(ctx) }()
	waitFor(t, "a retried advertise", func() bool { return publisher.count() >= 2 })
	waitFor(t, "the recorded failure", func() bool {
		return a.PeerCandidates().LastAdvertiseError != ""
	})
	if !a.PeerCandidates().LastAdvertisedAt.IsZero() {
		t.Error("a failed advertise must not look like a successful publish")
	}
	cancel()
	<-done
}

// Interface addresses are filtered to the families the coordinator accepts,
// deduplicated, sorted and capped at the server's MAX_LAN_ADDRESSES, because one
// unacceptable entry refuses the entire atomic replacement.
func TestAdvertisableAddressesMatchesTheServerContract(t *testing.T) {
	addrs := []netip.Addr{
		netip.MustParseAddr("203.0.113.7"),  // public: the WAN address is observed.
		netip.MustParseAddr("127.0.0.1"),    // loopback is in none of the families.
		netip.MustParseAddr("fd00::1"),      // IPv6 ULA is deliberately excluded.
		netip.MustParseAddr("192.168.1.22"), // RFC1918.
		netip.MustParseAddr("192.168.1.22"), // duplicate across two interfaces.
		netip.MustParseAddr("100.64.3.4"),   // CGNAT.
		netip.MustParseAddr("169.254.5.6"),  // link-local IPv4.
		netip.MustParseAddr("fe80::1"),      // link-local IPv6.
	}
	got := advertisableAddresses(addrs, 7443)
	if len(got) != 4 {
		t.Fatalf("advertisable = %+v, want the four accepted families once each", got)
	}
	for i, a := range got {
		if a.Kind != "lan" || a.Port != 7443 {
			t.Fatalf("addresses[%d] = %+v", i, a)
		}
		if i > 0 && got[i-1].Address > a.Address {
			t.Fatalf("addresses are not sorted: %+v", got)
		}
	}
	if len(advertisableAddresses(addrs, 0)) != 0 {
		t.Error("a zero port has no dial candidate to publish")
	}
	many := make([]netip.Addr, 0, 20)
	for i := range 20 {
		many = append(many, netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)}))
	}
	capped := advertisableAddresses(many, 7443)
	if len(capped) != client.MaxAdvertisedLANAddresses {
		t.Fatalf("capped = %d, want the server cap %d", len(capped), client.MaxAdvertisedLANAddresses)
	}
}

// The LAN candidate set is unauthenticated input from whoever is on the network,
// so it is bounded and it expires.
func TestCandidateSetIsBoundedAndExpires(t *testing.T) {
	now := time.Now()
	set := newCandidateSet(func() time.Time { return now })
	for i := range maxLANCandidates + 50 {
		fingerprint := string([]byte{byte('a' + i%6)})
		for len(fingerprint) < 64 {
			fingerprint += fingerprint
		}
		set.add([]discovery.Candidate{{HostID: "h", Fingerprint: fingerprint[:64],
			Port: 7443, SeenAt: now}})
	}
	if got := len(set.list(now)); got > maxLANCandidates {
		t.Fatalf("candidate set grew to %d, above the %d bound", got, maxLANCandidates)
	}
	set.add([]discovery.Candidate{{HostID: "h9", Fingerprint: mdnsOnlyFingerprint,
		Port: 7443, SeenAt: now}})
	if len(set.list(now.Add(candidateTTL+time.Second))) != 0 {
		t.Fatal("stale LAN candidates were not dropped")
	}
	// Malformed claims never enter at all: a fingerprint we cannot pin is a peer
	// we could only dial unauthenticated.
	set.add([]discovery.Candidate{{HostID: "h", Fingerprint: "nope", Port: 7443, SeenAt: now}})
	set.add([]discovery.Candidate{{HostID: "bad host", Fingerprint: mdnsOnlyFingerprint,
		Port: 7443, SeenAt: now}})
	for _, c := range set.list(now) {
		if !discovery.ValidFingerprint(c.Fingerprint) || !discovery.ValidHostID(c.HostID) {
			t.Fatalf("a malformed candidate was retained: %+v", c)
		}
	}
}

// Run starts discovery alongside the heartbeat and attempt loops, and returns
// cleanly when the context is cancelled — the wiring itself, not just the loop.
func TestRunStartsAndStopsDiscovery(t *testing.T) {
	publisher := &fakePublisher{}
	directory := &fakeDirectory{directory: oneAuthorizedPeer()}
	a := discoveryAgent(t, DiscoveryOptions{Publisher: publisher, Source: directory})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitFor(t, "an advertise from Run", func() bool { return publisher.count() >= 1 })
	waitFor(t, "a directory read from Run", func() bool { return directory.count() >= 1 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// staticOnlyFingerprint is configured by the owner and listed by nobody.
const staticOnlyFingerprint = "4444444444444444444444444444444444444444444444444444444444444444"

// The wiring-layer counterpart of discovery.TestStaticPeerNeverEntersAllowedPeers.
// A static endpoint is the owner saying WHERE a peer is, never WHO may act as one:
// §30.2 holds at the layer where a shortcut would actually be taken.
func TestAgentStaticPeerNeverEntersAllowedPeers(t *testing.T) {
	directory := &fakeDirectory{directory: oneAuthorizedPeer()}
	static := []discovery.Static{
		// Configured but unauthorized: display only.
		{Fingerprint: staticOnlyFingerprint, Address: netip.MustParseAddrPort("10.20.0.5:7443"), Label: "vlan 20"},
		// Configured AND authorized: contributes a cross-VLAN dial address.
		{Fingerprint: coordinatorFingerprint, Address: netip.MustParseAddrPort("10.20.0.6:7443")},
	}
	a := discoveryAgent(t, DiscoveryOptions{Publisher: &fakePublisher{}, Source: directory, StaticPeers: static})
	rendezvous, err := discovery.NewRendezvous(discovery.RendezvousOptions{
		Source: directory, Sharing: discovery.SharingPolicy{WANRendezvous: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rendezvous.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a.mergePeers(rendezvous, newCandidateSet(time.Now), discovery.NewLinkView(nil), now)
	view := a.PeerCandidates()
	if len(view.Allowed) != 1 || view.Allowed[0] != coordinatorFingerprint {
		t.Fatalf("allowed = %v, want only the coordinator's peer", view.Allowed)
	}
	var sawConfiguredUnauthorized, sawCrossVLANAddress bool
	for _, p := range view.Peers {
		switch p.Fingerprint {
		case staticOnlyFingerprint:
			sawConfiguredUnauthorized = true
			if p.Authorized || !p.Configured {
				t.Fatalf("configured peer = %+v; configuration must not authorize", p)
			}
		case coordinatorFingerprint:
			if !p.Configured || len(p.Addresses) == 0 || p.Addresses[0].String() != "10.20.0.6:7443" {
				t.Fatalf("authorized peer = %+v; the configured cross-VLAN address should lead", p)
			}
			sawCrossVLANAddress = true
		}
	}
	if !sawConfiguredUnauthorized || !sawCrossVLANAddress {
		t.Fatalf("view = %+v", view.Peers)
	}
}

// Static peers must still be merged when both discovery gates are shut: the owner
// typed those addresses, and mDNS being off is not a reason to hide them. Nothing
// is announced or polled in that state, so nothing can become authorized either.
func TestAgentStaticPeersMergeWithDiscoveryGatesShut(t *testing.T) {
	a, _ := testAgent(t)
	a.discovery = &DiscoveryOptions{
		Config:        &config.Discovery{},
		Sharing:       discovery.SharingPolicy{},
		StaticPeers:   []discovery.Static{{Fingerprint: staticOnlyFingerprint, Address: netip.MustParseAddrPort("10.20.0.5:7443")}},
		MergeInterval: 5 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); a.runDiscovery(ctx) }()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(a.PeerCandidates().Peers) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	view := a.PeerCandidates()
	cancel()
	<-done
	if len(view.Peers) != 1 || view.Peers[0].Authorized || !view.Peers[0].Configured {
		t.Fatalf("view = %+v; a configured peer must be visible and unauthorized with the gates shut", view.Peers)
	}
	if len(view.Allowed) != 0 {
		t.Fatalf("allowed = %v, want empty", view.Allowed)
	}
}

func TestStaticPeersFromDropsUnusableEntries(t *testing.T) {
	out := StaticPeersFrom([]config.StaticPeer{
		{Endpoint: "https://10.20.0.5:8443", Fingerprint: staticOnlyFingerprint},
		{Endpoint: "not-a-url", Fingerprint: staticOnlyFingerprint},
		{Endpoint: "https://10.20.0.7:8443", Fingerprint: "short"},
	})
	if len(out) != 1 || out[0].Address.String() != "10.20.0.5:8443" {
		t.Fatalf("out = %+v", out)
	}
}
