package pool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/discovery"
)

// Everything in this file runs a real ring over real TLS 1.3 connections on
// loopback, with real mutual authentication against the real registry. What it
// cannot do is stated here rather than implied by a green run: there is no second
// Mac, no Apple silicon, no Thunderbolt hardware and no physical network in this
// environment, and MLX is not installed and cannot be. So nothing here measures
// throughput, nothing here exercises a real LAN or a cable, and nothing here
// compares this collective's numbers against MLX's own `ring` backend — which
// HARDENING-PLAN §42.6 requires before agreement with MLX may be claimed.

const testTenant = "tenant-alpha"

type ringSet struct {
	registry   *Registry
	identities []Identity
	tenant     string
	fprints    []string
	endpoints  []string
	rings      []*Ring
}

// newRingSet binds every rank's listener first so that each rank's member list
// can carry real endpoints, then constructs the rings, then serves the ones the
// caller marked alive. A rank marked dead has its listener closed, so dialing it
// is refused by the kernel — the cheapest honest stand-in for a machine that went
// away, and the one that does not depend on a timeout to be detected.
func newRingSet(t *testing.T, n int, tenant string, step time.Duration, alive []bool) *ringSet {
	t.Helper()
	registry := NewRegistry(nil)
	set := &ringSet{registry: registry, tenant: tenant}
	identities := make([]Identity, n)
	defer func() { set.identities = identities }()
	listeners := make([]net.Listener, n)
	for i := range n {
		identities[i] = enrolledIdentity(t, registry)
		set.fprints = append(set.fprints, DeviceID(identities[i].PublicKey))
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i] = listener
		set.endpoints = append(set.endpoints, "https://"+listener.Addr().String())
	}
	for i := range n {
		var members []RingMember
		var authorized []string
		for j := range n {
			if j == i {
				members = append(members, RingMember{Fingerprint: set.fprints[j], Tenant: tenant})
				continue
			}
			members = append(members, RingMember{Fingerprint: set.fprints[j], Tenant: tenant, Endpoint: set.endpoints[j]})
			authorized = append(authorized, set.fprints[j])
		}
		ring, err := NewRing(RingOptions{Identity: identities[i], Registry: registry, Tenant: tenant,
			AuthorizedPeers: authorized, Members: members, StepTimeout: step})
		if err != nil {
			t.Fatalf("rank %d: %v", i, err)
		}
		set.rings = append(set.rings, ring)
		if alive[i] {
			done := make(chan error, 1)
			go func() { done <- ring.Serve(listeners[i]) }()
			// Wait for the listener to be owned before the test can shut it down:
			// a Shutdown that overtakes Serve makes Serve refuse the listener.
			for waited := 0; !ring.transport.serving(); waited++ {
				if waited > 2000 {
					t.Fatal("ring server did not start")
				}
				time.Sleep(time.Millisecond)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := ring.Shutdown(ctx); err != nil {
					_ = ring.Close()
				}
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, http.ErrServerClosed) {
						t.Errorf("rank serve: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Error("ring server did not stop")
				}
			})
			continue
		}
		if err := listeners[i].Close(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ring.Close() })
	}
	return set
}

// ringOf returns the Ring whose identity is the given index in the set, and its
// rank. Rank is not the construction index: it is the position in the ring's
// deterministic fingerprint ordering.
func (s *ringSet) ringOf(index int) (*Ring, int) {
	return s.rings[index], s.rings[index].Rank()
}

// runAll runs AllReduce on the given ranks concurrently and fails the test rather
// than hanging if any of them does not return. The watchdog is the point: a test
// that hangs on a deadlock reports "timed out" minutes later with no rank named.
func runAll(t *testing.T, session string, op ReduceOp, budget time.Duration,
	rings []*Ring, vectors [][]float64) []error {
	t.Helper()
	errs := make([]error, len(rings))
	var wg sync.WaitGroup
	for i, ring := range rings {
		wg.Add(1)
		go func(i int, ring *Ring) {
			defer wg.Done()
			errs[i] = ring.AllReduce(context.Background(), session, vectors[i], op)
		}(i, ring)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(budget):
		t.Fatalf("the collective did not return within %s: it deadlocked", budget)
	}
	return errs
}

func TestRingAllReduceMatchesSerialReductionOverRealTLS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ranks  int
		length int
		op     ReduceOp
	}{
		// A length that is not a multiple of the rank count exercises the
		// remainder in chunkBounds, which is where an off-by-one would hide.
		{"sum over three ranks", 3, 10, ReduceSum},
		{"max over four ranks", 4, 7, ReduceMax},
		{"min over two ranks", 2, 5, ReduceMin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alive := make([]bool, tc.ranks)
			for i := range alive {
				alive[i] = true
			}
			set := newRingSet(t, tc.ranks, testTenant, 5*time.Second, alive)
			vectors := make([][]float64, tc.ranks)
			for i := range tc.ranks {
				vectors[i] = make([]float64, tc.length)
				for j := range tc.length {
					// Distinct per (rank, element) so a mixed-up chunk cannot
					// coincidentally produce the right answer.
					vectors[i][j] = float64(i+1)*100 + float64(j) - float64(i*j)
				}
			}
			want := make([]float64, tc.length)
			copy(want, vectors[0])
			for i := 1; i < tc.ranks; i++ {
				for j := range tc.length {
					want[j] = tc.op.apply(want[j], vectors[i][j])
				}
			}
			for _, err := range runAll(t, testSession, tc.op, 30*time.Second, set.rings, vectors) {
				if err != nil {
					t.Fatalf("all-reduce failed: %v", err)
				}
			}
			for i := range tc.ranks {
				if !slices.Equal(vectors[i], want) {
					t.Fatalf("rank %d holds %v, want %v", i, vectors[i], want)
				}
			}
		})
	}
}

// The requirement stated as a test: a dead rank must not deadlock the collective.
// Every survivor must return an error that names the rank that failed, and it must
// do so without waiting out a long timeout — the survivor that cannot reach the
// dead rank tells the others, so they fail on the report rather than on the clock.
func TestRingDeadRankDoesNotHangTheCollectiveAndIsNamed(t *testing.T) {
	const ranks = 3
	set := newRingSet(t, ranks, testTenant, 4*time.Second, []bool{true, false, true})
	dead, deadRank := set.ringOf(1)
	_ = dead
	survivors := []*Ring{set.rings[0], set.rings[2]}
	vectors := [][]float64{{1, 2, 3, 4}, {5, 6, 7, 8}}
	started := time.Now()
	errs := runAll(t, testSession, ReduceSum, 20*time.Second, survivors, vectors)
	elapsed := time.Since(started)
	reported := 0
	for i, err := range errs {
		if err == nil {
			t.Fatalf("survivor %d reported success while a rank was dead", i)
		}
		if errors.Is(err, ErrAborted) {
			reported++
		}
		var failure *RingFailure
		if !errors.As(err, &failure) {
			t.Fatalf("survivor %d returned %v, which does not name a rank", i, err)
		}
		if failure.Rank != deadRank {
			t.Errorf("survivor %d blamed rank %d, the dead rank is %d", i, failure.Rank, deadRank)
		}
		if failure.Fingerprint != set.fprints[1] {
			t.Errorf("survivor %d named fingerprint %q, want %q", i, failure.Fingerprint, set.fprints[1])
		}
		if !errors.Is(err, ErrRankUnreachable) && !errors.Is(err, ErrAborted) {
			t.Errorf("survivor %d error is neither unreachable nor aborted: %v", i, err)
		}
	}
	// The survivor whose successor is the dead rank cannot reach it and fails
	// immediately; the survivor waiting on the dead rank learns from that report.
	// Exactly one of the two therefore fails on an abort rather than on a clock.
	if reported != 1 {
		t.Errorf("%d survivors learned from a failure report, want 1", reported)
	}
	// One step timeout is 4 s. Finishing well inside that is the proof that no
	// survivor had to wait out its own timeout to notice.
	if elapsed > 3*time.Second {
		t.Errorf("survivors took %s to give up; the failure report is not reaching them", elapsed)
	}
	// The ring must still be usable afterwards for a different session: a failed
	// collective may not poison the transport.
	if err := set.rings[0].transport.authorized(); err != nil {
		t.Errorf("transport unusable after a rank failure: %v", err)
	}
}

// Oversized input is refused rather than allocated, at both boundaries: the local
// API refuses a vector above the bound, and the inbox refuses a body above the
// frame ceiling on its declared length before reading it.
func TestRingRefusesOversizedInput(t *testing.T) {
	set := newRingSet(t, 2, testTenant, 3*time.Second, []bool{true, true})
	ring := set.rings[0]
	if err := ring.AllReduce(context.Background(), testSession,
		make([]float64, MaxCollectiveValues+1), ReduceSum); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized vector returned %v, want ErrInvalid", err)
	}
	// Fewer values than ranks would mean an empty chunk, which this protocol
	// cannot express; refusing beats degrading to something else.
	if err := ring.AllReduce(context.Background(), testSession, []float64{1}, ReduceSum); !errors.Is(err, ErrInvalid) {
		t.Fatalf("undersized vector returned %v, want ErrInvalid", err)
	}
	if err := ring.AllReduce(context.Background(), "not-a-session", []float64{1, 2}, ReduceSum); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid session returned %v, want ErrInvalid", err)
	}
	client := ring.transport.clients[set.fprints[1]]
	req, err := http.NewRequest(http.MethodPost, client.endpoint+collectivePath,
		bytes.NewReader(make([]byte, maxCollectiveFrameBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.client.Do(req)
	if err != nil {
		t.Fatalf("oversized post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized frame answered HTTP %d, want 413", resp.StatusCode)
	}
}

// Rank identity is bound to the authenticated fingerprint, and tenant scope is
// checked on every frame rather than only at construction.
func TestRingInboxRefusesSpoofedRankAndForeignTenant(t *testing.T) {
	set := newRingSet(t, 2, testTenant, 3*time.Second, []bool{true, true})
	ring := set.rings[0]
	successor := ring.Successor().Fingerprint
	base := collectiveFrame{kind: collectiveKindChunk, phase: collectivePhaseReduceScatter,
		op: ReduceSum, senderRank: ring.Rank(), step: 0, chunk: 0,
		session: testSession, tenant: testTenant, total: 2, values: []float64{1}}
	if err := ring.transport.send(context.Background(), time.Second, base, successor); err != nil {
		t.Fatalf("a well-formed frame was refused: %v", err)
	}
	spoofed := base
	spoofed.senderRank = (ring.Rank() + 1) % 2
	spoofed.session = strings.Repeat("a", 64)
	if err := ring.transport.send(context.Background(), time.Second, spoofed, successor); err == nil ||
		!strings.Contains(err.Error(), "403") {
		t.Fatalf("frame claiming another rank returned %v, want an HTTP 403", err)
	}
	foreign := base
	foreign.tenant = "tenant-beta"
	foreign.session = strings.Repeat("b", 64)
	if err := ring.transport.send(context.Background(), time.Second, foreign, successor); err == nil ||
		!strings.Contains(err.Error(), "403") {
		t.Fatalf("frame from another tenant returned %v, want an HTTP 403", err)
	}
	outOfRange := base
	outOfRange.step = 1 // A two-rank ring runs exactly one step per phase.
	outOfRange.session = strings.Repeat("c", 64)
	if err := ring.transport.send(context.Background(), time.Second, outOfRange, successor); err == nil {
		t.Fatal("a step outside the algorithm's range was accepted")
	}
}

// DISCOVERY NEVER AUTHORIZES, driven through the real discovery merge rather than
// asserted about it: a peer seen only on mDNS is absent from AllowedPeers, and
// NewRing refuses it even though it is reachable, has a valid fingerprint and a
// dialable endpoint. Physical presence on the link — including a Thunderbolt
// cable (§49.4) — grants nothing.
func TestRingMDNSOnlyPeerNeverEntersTheRing(t *testing.T) {
	registry := NewRegistry(nil)
	self := enrolledIdentity(t, registry)
	listed := enrolledIdentity(t, registry)
	// Enrolled in the registry and announcing itself on the local link, but never
	// listed by the coordinator. That is exactly the café/hotel/office case.
	lan := enrolledIdentity(t, registry)
	listedFingerprint := DeviceID(listed.PublicKey)
	lanFingerprint := DeviceID(lan.PublicKey)

	snapshot := discovery.Snapshot{Peers: []discovery.AuthorizedPeer{{
		HostID: "h_0123456789abcdef", Name: "studio", Fingerprint: listedFingerprint,
		Addresses: []discovery.AddressClaim{{Kind: "lan", Address: "127.0.0.1", Port: 7443}},
	}}}
	candidates := []discovery.Candidate{
		{HostID: "h_0123456789abcdef", Fingerprint: listedFingerprint, Port: 7443,
			From: netip.MustParseAddr("127.0.0.1"), SeenAt: time.Now()},
		{HostID: "h_fedcba9876543210", Fingerprint: lanFingerprint, Port: 7443,
			From: netip.MustParseAddr("127.0.0.1"), SeenAt: time.Now()},
	}
	peers := discovery.Merge(snapshot, candidates, discovery.NewLinkView(nil), time.Now())
	allowed := discovery.AllowedPeers(peers)
	if !slices.Contains(allowed, listedFingerprint) {
		t.Fatalf("the coordinator's peer is missing from AllowedPeers: %v", allowed)
	}
	if slices.Contains(allowed, lanFingerprint) {
		t.Fatalf("an mDNS-only peer reached AllowedPeers: %v", allowed)
	}
	options := RingOptions{Identity: self, Registry: registry, Tenant: testTenant,
		AuthorizedPeers: allowed, Members: []RingMember{
			{Fingerprint: DeviceID(self.PublicKey), Tenant: testTenant},
			{Fingerprint: listedFingerprint, Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"},
			{Fingerprint: lanFingerprint, Tenant: testTenant, Endpoint: "https://127.0.0.1:7444"},
		}}
	ring, err := NewRing(options)
	if err == nil {
		ring.Close()
		t.Fatal("a ring accepted a peer the coordinator never authorized")
	}
	if !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), lanFingerprint) {
		t.Fatalf("refusal did not name the unauthorized peer: %v", err)
	}
	// With the mDNS-only peer removed, the same construction succeeds — the
	// refusal is about authorization, not about the peer being unreachable.
	options.Members = options.Members[:2]
	ring, err = NewRing(options)
	if err != nil {
		t.Fatalf("the coordinator's own peer was refused: %v", err)
	}
	defer ring.Close()
	if ring.Size() != 2 {
		t.Fatalf("ring size %d, want 2", ring.Size())
	}
}

func TestRingRefusesCrossTenantAndUnscopedMembership(t *testing.T) {
	registry := NewRegistry(nil)
	self := enrolledIdentity(t, registry)
	peer := enrolledIdentity(t, registry)
	peerFingerprint := DeviceID(peer.PublicKey)
	base := RingOptions{Identity: self, Registry: registry, Tenant: testTenant,
		AuthorizedPeers: []string{peerFingerprint}, Members: []RingMember{
			{Fingerprint: DeviceID(self.PublicKey), Tenant: testTenant},
			{Fingerprint: peerFingerprint, Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"},
		}}
	if ring, err := NewRing(base); err != nil {
		t.Fatalf("same-tenant ring refused: %v", err)
	} else {
		ring.Close()
	}
	crossTenant := base
	crossTenant.Members = []RingMember{base.Members[0],
		{Fingerprint: peerFingerprint, Tenant: "tenant-beta", Endpoint: "https://127.0.0.1:7443"}}
	if ring, err := NewRing(crossTenant); !errors.Is(err, ErrUnauthorized) {
		if err == nil {
			ring.Close()
		}
		t.Fatalf("cross-tenant member returned %v, want ErrUnauthorized", err)
	}
	selfCrossTenant := base
	selfCrossTenant.Members = []RingMember{
		{Fingerprint: DeviceID(self.PublicKey), Tenant: "tenant-beta"}, base.Members[1]}
	if ring, err := NewRing(selfCrossTenant); !errors.Is(err, ErrUnauthorized) {
		if err == nil {
			ring.Close()
		}
		t.Fatalf("this host scoped to another tenant returned %v, want ErrUnauthorized", err)
	}
	// Tenant scope is mandatory: an empty scope is an error, never a wildcard.
	unscoped := base
	unscoped.Tenant = ""
	unscoped.Members = []RingMember{{Fingerprint: DeviceID(self.PublicKey)},
		{Fingerprint: peerFingerprint, Endpoint: "https://127.0.0.1:7443"}}
	if ring, err := NewRing(unscoped); !errors.Is(err, ErrInvalid) {
		if err == nil {
			ring.Close()
		}
		t.Fatalf("unscoped ring returned %v, want ErrInvalid", err)
	}
}

func TestRingRefusesMalformedMembership(t *testing.T) {
	registry := NewRegistry(nil)
	self := enrolledIdentity(t, registry)
	peer := enrolledIdentity(t, registry)
	stranger := enrolledIdentity(t, registry)
	selfFingerprint := DeviceID(self.PublicKey)
	peerFingerprint := DeviceID(peer.PublicKey)
	valid := []RingMember{{Fingerprint: selfFingerprint, Tenant: testTenant},
		{Fingerprint: peerFingerprint, Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"}}
	cases := map[string]RingOptions{
		"one rank": {Members: valid[:1], AuthorizedPeers: []string{peerFingerprint}},
		"this host absent": {Members: []RingMember{
			{Fingerprint: peerFingerprint, Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"},
			{Fingerprint: DeviceID(stranger.PublicKey), Tenant: testTenant, Endpoint: "https://127.0.0.1:7444"}},
			AuthorizedPeers: []string{peerFingerprint, DeviceID(stranger.PublicKey)}},
		"duplicate member": {Members: []RingMember{valid[0], valid[1], valid[1]},
			AuthorizedPeers: []string{peerFingerprint}},
		"fingerprint is a name": {Members: []RingMember{valid[0],
			{Fingerprint: "studio.local", Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"}},
			AuthorizedPeers: []string{peerFingerprint}},
		"uppercase fingerprint": {Members: []RingMember{valid[0],
			{Fingerprint: strings.ToUpper(peerFingerprint), Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"}},
			AuthorizedPeers: []string{peerFingerprint}},
		"authorized list holds a name": {Members: valid, AuthorizedPeers: []string{"studio.local"}},
		"public endpoint": {Members: []RingMember{valid[0],
			{Fingerprint: peerFingerprint, Tenant: testTenant, Endpoint: "https://203.0.113.7:7443"}},
			AuthorizedPeers: []string{peerFingerprint}},
		"plaintext endpoint": {Members: []RingMember{valid[0],
			{Fingerprint: peerFingerprint, Tenant: testTenant, Endpoint: "http://127.0.0.1:7443"}},
			AuthorizedPeers: []string{peerFingerprint}},
		"endpoint without a port": {Members: []RingMember{valid[0],
			{Fingerprint: peerFingerprint, Tenant: testTenant, Endpoint: "https://127.0.0.1"}},
			AuthorizedPeers: []string{peerFingerprint}},
		"hostname endpoint": {Members: []RingMember{valid[0],
			{Fingerprint: peerFingerprint, Tenant: testTenant, Endpoint: "https://studio.local:7443"}},
			AuthorizedPeers: []string{peerFingerprint}},
		"endpoint for this host": {Members: []RingMember{
			{Fingerprint: selfFingerprint, Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"}, valid[1]},
			AuthorizedPeers: []string{peerFingerprint}},
		"step timeout above the bound": {Members: valid, AuthorizedPeers: []string{peerFingerprint},
			StepTimeout: maxRingStepTimeout + time.Second},
	}
	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			options.Identity, options.Registry, options.Tenant = self, registry, testTenant
			ring, err := NewRing(options)
			if err == nil {
				ring.Close()
				t.Fatalf("a ring was built with %s", name)
			}
		})
	}
	// A ring above the rank bound is refused rather than truncated.
	over := RingOptions{Identity: self, Registry: registry, Tenant: testTenant}
	for i := range MaxRingRanks + 1 {
		fingerprint := fmt.Sprintf("%064x", i+1)
		over.Members = append(over.Members, RingMember{Fingerprint: fingerprint,
			Tenant: testTenant, Endpoint: "https://127.0.0.1:7443"})
		over.AuthorizedPeers = append(over.AuthorizedPeers, fingerprint)
	}
	over.Members = append(over.Members, RingMember{Fingerprint: selfFingerprint, Tenant: testTenant})
	if ring, err := NewRing(over); !errors.Is(err, ErrInvalid) {
		if err == nil {
			ring.Close()
		}
		t.Fatalf("ring above %d ranks returned %v, want ErrInvalid", MaxRingRanks, err)
	}
}

// A revocation between construction and use stops the collective. Membership is
// re-checked against the registry, not cached from construction.
func TestRingRefusesRevokedMemberBeforeRunning(t *testing.T) {
	set := newRingSet(t, 2, testTenant, 2*time.Second, []bool{true, true})
	if err := set.registry.Revoke(set.fprints[1]); err != nil {
		t.Fatal(err)
	}
	err := set.rings[0].AllReduce(context.Background(), testSession, []float64{1, 2}, ReduceSum)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("collective with a revoked member returned %v, want ErrUnauthorized", err)
	}
}

// Rank ordering must be identical on every rank and derived from nothing a peer
// or a network can influence.
func TestRingRankOrderIsDeterministicAndFingerprintDerived(t *testing.T) {
	alive := []bool{true, true, true, true}
	set := newRingSet(t, 4, testTenant, time.Second, alive)
	expected := slices.Clone(set.fprints)
	slices.Sort(expected)
	for i, ring := range set.rings {
		var got []string
		for _, m := range ring.Members() {
			got = append(got, m.Fingerprint)
		}
		if !slices.Equal(got, expected) {
			t.Fatalf("rank view %d = %v, want %v", i, got, expected)
		}
		if ring.Members()[ring.Rank()].Fingerprint != set.fprints[i] {
			t.Fatalf("rank %d is not this host's position", ring.Rank())
		}
		if ring.Successor().Fingerprint != expected[(ring.Rank()+1)%4] ||
			ring.Predecessor().Fingerprint != expected[(ring.Rank()+3)%4] {
			t.Fatalf("rank %d has the wrong neighbours", ring.Rank())
		}
	}
	// Members returns a copy: reordering it must not reorder the ring.
	view := set.rings[0].Members()
	slices.Reverse(view)
	if set.rings[0].Members()[0].Fingerprint != expected[0] {
		t.Fatal("mutating the returned member slice changed the ring")
	}
}

func TestRingAllReduceIsContextCancellable(t *testing.T) {
	set := newRingSet(t, 2, testTenant, 5*time.Second, []bool{true, true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- set.rings[0].AllReduce(ctx, testSession, []float64{1, 2, 3}, ReduceSum)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled collective returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled collective did not return")
	}
}

// A JACCL plan may not be run over this transport. JACCL is RDMA over Thunderbolt
// (§39.1) and does not run over TCP/IP at all (§39.2); accepting the plan here
// would be the silent downgrade §49.3 refuses.
func TestNewRingFromPlanRefusesJACCLAndPreservesPlanRanks(t *testing.T) {
	set := newRingSet(t, 3, testTenant, time.Second, []bool{true, true, true})
	options := func(index int) RingOptions {
		var members []RingMember
		var authorized []string
		for j := range 3 {
			if j == index {
				members = append(members, RingMember{Fingerprint: set.fprints[j], Tenant: testTenant})
				continue
			}
			members = append(members, RingMember{Fingerprint: set.fprints[j], Tenant: testTenant, Endpoint: set.endpoints[j]})
			authorized = append(authorized, set.fprints[j])
		}
		return RingOptions{Identity: Identity{}, Registry: set.registry, Tenant: testTenant,
			AuthorizedPeers: authorized, Members: members}
	}
	// The plan's order is deliberately not the fingerprint order.
	order := slices.Clone(set.fprints)
	slices.Sort(order)
	slices.Reverse(order)
	plan := PlacementPlan{Scope: "private-lan", Backend: TCPRing}
	for i, fingerprint := range order {
		plan.Ranks = append(plan.Ranks, RankPlacement{Rank: i, DeviceID: fingerprint})
	}
	jaccl := plan
	jaccl.Backend = JACCL
	o := options(0)
	o.Identity = set.rings[0].transport.identity
	if ring, err := NewRingFromPlan(o, jaccl); !errors.Is(err, ErrUnsupported) {
		if err == nil {
			ring.Close()
		}
		t.Fatalf("a JACCL plan returned %v, want ErrUnsupported", err)
	}
	ring, err := NewRingFromPlan(o, plan)
	if err != nil {
		t.Fatalf("a tcp-ring plan was refused: %v", err)
	}
	defer ring.Close()
	for i, m := range ring.Members() {
		if m.Fingerprint != order[i] {
			t.Fatalf("plan rank %d holds %s, the plan placed %s", i, m.Fingerprint, order[i])
		}
	}
	// A plan that places a device this host has no authorized member for is
	// refused rather than partially run.
	foreign := plan
	foreign.Ranks[1].DeviceID = strings.Repeat("f", 64)
	if built, err := NewRingFromPlan(o, foreign); !errors.Is(err, ErrUnauthorized) {
		if err == nil {
			built.Close()
		}
		t.Fatalf("a plan naming an unauthorized device returned %v, want ErrUnauthorized", err)
	}
	// A non-contiguous plan is a plan this ring cannot honour.
	broken := PlacementPlan{Scope: "private-lan", Backend: TCPRing, Ranks: []RankPlacement{
		{Rank: 1, DeviceID: order[0]}, {Rank: 0, DeviceID: order[1]}, {Rank: 2, DeviceID: order[2]}}}
	if built, err := NewRingFromPlan(o, broken); err == nil {
		built.Close()
		t.Fatal("a plan with non-contiguous ranks was accepted")
	}
}

// The inbox is what keeps the ring from deadlocking on itself: a chunk that
// arrives before the local rank reaches that step is held, not dropped, and a
// duplicate for the same step is refused rather than overwriting the first.
func TestRingInboxHoldsEarlyFramesAndRefusesDuplicates(t *testing.T) {
	inbox := newRingInbox(time.Now)
	frame := testChunkFrame()
	if err := inbox.deliver(frame); err != nil {
		t.Fatalf("early frame rejected: %v", err)
	}
	if err := inbox.deliver(frame); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate step returned %v, want ErrConflict", err)
	}
	session, err := inbox.join(frame.session, frame.total, frame.op)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	got, err := session.wait(context.Background(), time.Second, frame.phase, frame.step)
	if err != nil || len(got.values) != len(frame.values) {
		t.Fatalf("held frame not delivered: %+v %v", got, err)
	}
	// A vector length or reduction that disagrees with the session is refused.
	other := frame
	other.step = 0
	other.total = frame.total + 1
	if err := inbox.deliver(other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("frame disagreeing about the vector length returned %v", err)
	}
	other.total, other.op = frame.total, ReduceMax
	if err := inbox.deliver(other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("frame disagreeing about the reduction returned %v", err)
	}
	// A wait with nothing to wait for ends on the timeout, not never.
	started := time.Now()
	if _, err := session.wait(context.Background(), 100*time.Millisecond,
		collectivePhaseAllGather, 0); !errors.Is(err, ErrTimeout) {
		t.Fatalf("empty wait returned %v, want ErrTimeout", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("the bounded wait was not bounded")
	}
	inbox.leave(frame.session)
	if _, err := inbox.session(frame.session, false); err == nil {
		t.Fatal("leaving did not release the session")
	}
}

// Session slots are bounded, because a session is created by a remote frame.
func TestRingInboxBoundsSessionsAndPendingBytes(t *testing.T) {
	inbox := newRingInbox(time.Now)
	for i := range maxCollectiveSessions {
		frame := testChunkFrame()
		frame.session = fmt.Sprintf("%064x", i)
		if err := inbox.deliver(frame); err != nil {
			t.Fatalf("session %d rejected: %v", i, err)
		}
	}
	overflow := testChunkFrame()
	overflow.session = fmt.Sprintf("%064x", maxCollectiveSessions)
	if err := inbox.deliver(overflow); !errors.Is(err, ErrQuota) {
		t.Fatalf("session above the cap returned %v, want ErrQuota", err)
	}
	// Pending payload is capped per session: a member that posts full-size chunks
	// for every step cannot make this rank hold more than the ceiling.
	full := newRingInbox(time.Now)
	frame := testChunkFrame()
	frame.total = MaxCollectiveValues
	frame.values = make([]float64, MaxCollectiveValues)
	var refused bool
	for step := range MaxRingRanks {
		frame.step = step
		if err := full.deliver(frame); errors.Is(err, ErrQuota) {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatalf("pending payload was never capped at %d bytes", maxCollectivePendingBytes)
	}
}

// TestRestoredRegistryAuthorizesARealTwoRankAllReduce is the end-to-end property
// that persistence exists for.
//
// pool.Registry was in-memory only and had no production caller, so a host that
// restarted lost every peer it had enrolled and could not rejoin a ring it was
// already a member of. The collective's math and transport were both already
// tested; this asserts the missing half -- that membership which has been through
// Snapshot and RestoreRegistry is still sufficient authority for a real
// authenticated collective over TLS, not merely a struct that survived a
// round trip.
func TestRestoredRegistryAuthorizesARealTwoRankAllReduce(t *testing.T) {
	const n = 2
	set := newRingSet(t, n, testTenant, time.Second, []bool{true, true})

	// Persist, then rebuild exactly as a restart would.
	restored, err := RestoreRegistry(nil, set.registry.Snapshot())
	if err != nil {
		t.Fatalf("RestoreRegistry: %v", err)
	}
	for _, fingerprint := range set.fprints {
		if _, ok := restored.Member(fingerprint); !ok {
			t.Fatalf("rank %s is missing from the restored registry", fingerprint)
		}
	}

	// Rebuild both ranks against the RESTORED registry and run a real collective
	// through it, so the assertion covers the ring's live authorization checks.
	rings := make([]*Ring, n)
	listeners := make([]net.Listener, n)
	endpoints := make([]string, n)
	for i := range n {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		listeners[i] = listener
		endpoints[i] = "https://" + listener.Addr().String()
	}
	for i := range n {
		var members []RingMember
		var authorized []string
		for j := range n {
			if j == i {
				members = append(members, RingMember{Fingerprint: set.fprints[j], Tenant: testTenant})
				continue
			}
			members = append(members, RingMember{Fingerprint: set.fprints[j], Tenant: testTenant, Endpoint: endpoints[j]})
			authorized = append(authorized, set.fprints[j])
		}
		ring, err := NewRing(RingOptions{Identity: set.identities[i], Registry: restored,
			Tenant: testTenant, AuthorizedPeers: authorized, Members: members, StepTimeout: time.Second})
		if err != nil {
			t.Fatalf("rank %d against restored registry: %v", i, err)
		}
		rings[i] = ring
		go func() { _ = ring.Serve(listeners[i]) }()
		t.Cleanup(func() { _ = ring.Close() })
	}

	vectors := [][]float64{{1, 2, 3, 4}, {10, 20, 30, 40}}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = rings[i].AllReduce(context.Background(), testSession, vectors[i], ReduceSum)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("rank %d AllReduce: %v", i, err)
		}
	}
	want := []float64{11, 22, 33, 44}
	for rank := range n {
		for i := range want {
			if vectors[rank][i] != want[i] {
				t.Fatalf("rank %d = %v, want %v", rank, vectors[rank], want)
			}
		}
	}
}
