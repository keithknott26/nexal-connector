package p2p

// EVERY TEST IN THIS PACKAGE RUNS OFFLINE.
//
// The hosts are in-process libp2p hosts bound to /ip4/127.0.0.1/tcp/0. No test
// resolves a name, contacts a bootstrap peer, joins a DHT, or reaches a STUN or
// relay server on the internet — the production wiring in host.go has no DHT and
// no bootstrap list at all, which is what makes that property hold by
// construction rather than by test discipline. internal/stun took the same
// position in Phase 1 and this follows it.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"nexal/connector/internal/pool"
)

func newTestHost(t *testing.T, o Options) *Host {
	t.Helper()
	id, err := pool.NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	o.Enabled = true
	if o.Identity.PublicKey == nil {
		o.Identity = id
	}
	h, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func selfInfo(h *Host) peer.AddrInfo {
	return peer.AddrInfo{ID: h.PeerID(), Addrs: h.Addrs()}
}

// TestUnauthorizedPeerIsRefusedBeforeAnyData is the §30.2 invariant, stated as
// code: discovery and transport never establish membership. Host A's allowlist
// does not contain B. B is fully reachable, completes no connection, and opens no
// stream. "Reachable is not authorized" (peer.go:28) is the property under test.
func TestUnauthorizedPeerIsRefusedBeforeAnyData(t *testing.T) {
	// A authorizes nobody but itself.
	a := newTestHost(t, Options{Authorizer: NewCoordinatorAuthorizer(nil)})
	a.opts.Authorizer = NewCoordinatorAuthorizer([]string{a.DeviceID()})
	// B authorizes A, so any refusal comes from A's gate and not from B's.
	b := newTestHost(t, Options{Authorizer: NewCoordinatorAuthorizer([]string{a.DeviceID()})})

	// A must never see application data from B. The handler is registered so that
	// "the gate refused" and "the gate let it through but nothing happened to be
	// sent" cannot be confused.
	leaked := make(chan struct{}, 1)
	if err := a.Handle(func(s network.Stream) { leaked <- struct{}{}; _ = s.Reset() }); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Connect itself may return nil: libp2p completes the security handshake before
	// InterceptSecured runs, so the refusal shows up as an immediately torn-down
	// connection rather than as a dial error. The invariant is not "Connect errors",
	// it is "NO DATA FLOWS", so that is what is asserted.
	_ = b.Libp2pHost().Connect(ctx, selfInfo(a))
	if got := len(a.Libp2pHost().Network().ConnsToPeer(b.PeerID())); got != 0 {
		t.Fatalf("A holds %d connections to an unauthorized peer", got)
	}
	s, err := b.OpenStream(ctx, a.PeerID())
	if err == nil {
		_, _ = s.Write([]byte("data"))
		select {
		case <-leaked:
			t.Fatal("application data reached a host that never authorized the sender")
		case <-time.After(2 * time.Second):
			t.Fatal("a stream opened to a host that never authorized us")
		}
	}
	snap := a.Snapshot()
	if snap.DeniedTotal == 0 {
		t.Fatal("the refusal was not surfaced; an authorization gate that denies silently is undiagnosable")
	}
	found := false
	for _, d := range snap.Denials {
		if d.DeviceID == b.DeviceID() && d.Reason == "not in the coordinator's authorized set" {
			found = true
		}
	}
	if !found {
		t.Fatalf("denial record missing the peer and reason: %+v", snap.Denials)
	}
}

// TestAuthorizedPeerTransfersData is the positive control. Without it the test
// above is satisfied by a package that simply never connects to anything.
func TestAuthorizedPeerTransfersData(t *testing.T) {
	aid, _ := pool.NewIdentity()
	bid, _ := pool.NewIdentity()
	both := []string{pool.DeviceID(aid.PublicKey), pool.DeviceID(bid.PublicKey)}
	a := newTestHost(t, Options{Identity: aid, Authorizer: NewCoordinatorAuthorizer(both)})
	b := newTestHost(t, Options{Identity: bid, Authorizer: NewCoordinatorAuthorizer(both)})

	got := make(chan string, 1)
	if err := a.Handle(func(s network.Stream) {
		buf := make([]byte, 5)
		n, _ := s.Read(buf)
		got <- string(buf[:n])
		_ = s.Close()
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := b.Libp2pHost().Connect(ctx, selfInfo(a)); err != nil {
		t.Fatalf("authorized connect: %v", err)
	}
	s, err := b.OpenStream(ctx, a.PeerID())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if _, err := s.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case v := <-got:
		if v != "hello" {
			t.Fatalf("payload %q", v)
		}
	case <-ctx.Done():
		t.Fatal("no data arrived over an authorized direct connection")
	}
	// A DIRECT connection must not be metered. Metering the good path would
	// throttle the transport the relay ceiling exists to protect.
	if _, ok := s.(*MeteredStream); ok {
		t.Fatal("a direct stream was wrapped in the relay meter")
	}
	if c := b.Consumption(); c.RelayBytesUsed != 0 || c.RelayedCircuitsEver != 0 {
		t.Fatalf("direct transfer charged to the relay budget: %+v", c)
	}
}

// TestTransportCannotAddToTheAllowlist is the mirror image of the gate, and it is
// the invariant that actually keeps libp2p from becoming a back door: this package
// can refuse a peer but has no path by which a peer becomes authorized. The
// allowlist is byte-identical before and after a connection attempt.
func TestTransportCannotAddToTheAllowlist(t *testing.T) {
	authz := NewCoordinatorAuthorizer(nil)
	a := newTestHost(t, Options{Authorizer: authz})
	b := newTestHost(t, Options{Authorizer: NewCoordinatorAuthorizer([]string{a.DeviceID()})})
	before := authz.Snapshot()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = b.Libp2pHost().Connect(ctx, selfInfo(a))
	_, _ = b.OpenStream(ctx, a.PeerID())
	_ = a.Publish(ctx)
	_, _, _ = a.Connect(ctx, b.DeviceID())

	after := authz.Snapshot()
	if len(before) != 0 || len(after) != 0 {
		t.Fatalf("the allowlist changed through transport activity: %v -> %v", before, after)
	}
	if authz.Authorized(b.DeviceID()) {
		t.Fatal("a peer became authorized by connecting; AllowedPeers must come from the coordinator alone")
	}
}

// TestEmptyAllowlistDeniesEverything: a half-written config must fail closed. The
// LAN path takes the same position (pool.newPeerPolicy refuses len(allowed)<1).
func TestEmptyAllowlistDeniesEverything(t *testing.T) {
	g := newGater(NewCoordinatorAuthorizer(nil), mustLedger(t, Limits{}))
	id, _ := pool.NewIdentity()
	pid, err := PeerIDFor(id.PublicKey)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	if g.InterceptPeerDial(pid) {
		t.Fatal("empty allowlist permitted a dial")
	}
	if g.InterceptSecured(network.DirInbound, pid, nil) {
		t.Fatal("empty allowlist permitted a secured inbound connection")
	}
	// And a nil Authorizer — a wiring bug — must also deny rather than allow.
	nilg := newGater(nil, mustLedger(t, Limits{}))
	if nilg.InterceptSecured(network.DirInbound, pid, nil) {
		t.Fatal("a missing allowlist source permitted a connection")
	}
	if _, err := New(Options{Enabled: true, Identity: id}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an enabled host with no Authorizer was constructed: %v", err)
	}
}

// TestRevocationStopsStreamsOnALiveConnection: authorization is re-checked at
// stream time, not only at connect time, so a long-lived connection cannot outlive
// the coordinator's decision to revoke the device.
func TestRevocationStopsStreamsOnALiveConnection(t *testing.T) {
	aid, _ := pool.NewIdentity()
	bid, _ := pool.NewIdentity()
	aDevice, bDevice := pool.DeviceID(aid.PublicKey), pool.DeviceID(bid.PublicKey)
	aAuthz := NewCoordinatorAuthorizer([]string{aDevice, bDevice})
	a := newTestHost(t, Options{Identity: aid, Authorizer: aAuthz})
	b := newTestHost(t, Options{Identity: bid, Authorizer: NewCoordinatorAuthorizer([]string{aDevice, bDevice})})

	reached := make(chan struct{}, 4)
	if err := a.Handle(func(s network.Stream) { reached <- struct{}{}; _ = s.Close() }); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := b.Libp2pHost().Connect(ctx, selfInfo(a)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	first, err := b.OpenStream(ctx, a.PeerID())
	if err != nil {
		t.Fatalf("first stream: %v", err)
	}
	// Protocol negotiation is lazy: the handler on the far side does not run until
	// the first byte is written.
	if _, err := first.Write([]byte("x")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal("handler never ran for an authorized peer")
	}

	// Coordinator revokes B. The existing connection is still open.
	aAuthz.Replace([]string{aDevice})
	s, err := b.OpenStream(ctx, a.PeerID())
	if err != nil {
		// A's handler-side refusal may surface as a reset on the next read
		// rather than as a failed open; both are acceptable, silence is not.
		return
	}
	_, _ = s.Write([]byte("x"))
	buf := make([]byte, 1)
	_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := s.Read(buf); err == nil {
		t.Fatal("a revoked peer's stream stayed usable")
	}
	select {
	case <-reached:
		t.Fatal("the application handler ran for a revoked peer")
	default:
	}
}

// TestDisabledHostIsInert is the feature-flag invariant: shipping this cannot
// regress an existing user because for an existing user nothing runs.
func TestDisabledHostIsInert(t *testing.T) {
	id, _ := pool.NewIdentity()
	rz := NewStubRendezvous()
	h, err := New(Options{Enabled: false, Identity: id, Rendezvous: rz})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = h.Close() }()

	if h.Enabled() {
		t.Fatal("Enabled reported true for a disabled host")
	}
	if h.PeerID() != "" || len(h.Addrs()) != 0 || h.Libp2pHost() != nil {
		t.Fatal("a disabled host bound an address or built a libp2p host")
	}
	if h.DeviceID() != pool.DeviceID(id.PublicKey) {
		t.Fatal("DeviceID must stay meaningful while disabled; it is not a libp2p value")
	}
	ctx := context.Background()
	for name, err := range map[string]error{
		"Publish":    h.Publish(ctx),
		"Reserve":    h.Reserve(ctx, peer.AddrInfo{}),
		"Handle":     h.Handle(func(network.Stream) {}),
		"ForceClose": nil,
	} {
		if name != "ForceClose" && !errors.Is(err, ErrDisabled) {
			t.Fatalf("%s returned %v, want ErrDisabled", name, err)
		}
	}
	if _, _, err := h.Connect(ctx, pool.DeviceID(id.PublicKey)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Connect returned %v, want ErrDisabled", err)
	}
	if _, err := h.OpenStream(ctx, "x"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("OpenStream returned %v, want ErrDisabled", err)
	}
	if rz.Published() != nil {
		t.Fatal("a disabled host published an address; Phase 1's rule was that an address without a punching design must not leak")
	}
	if c := h.Consumption(); c.RelayBytesUsed != 0 {
		t.Fatal("a disabled host accounted relay bytes")
	}
	if s := h.Snapshot(); s.Enabled || s.PeerID != "" {
		t.Fatalf("disabled snapshot leaks state: %+v", s)
	}
}

// TestLANIsPreferredOverLibp2p: a peer reachable on a private address is left to
// the existing mutual-TLS transport. libp2p is the ADDITIONAL path, not a
// replacement, and the proven path must not be bypassed just because libp2p is
// available.
func TestLANIsPreferredOverLibp2p(t *testing.T) {
	aid, _ := pool.NewIdentity()
	bid, _ := pool.NewIdentity()
	both := []string{pool.DeviceID(aid.PublicKey), pool.DeviceID(bid.PublicKey)}
	rz := NewStubRendezvous()
	a := newTestHost(t, Options{Identity: aid, Authorizer: NewCoordinatorAuthorizer(both), Rendezvous: rz})
	b := newTestHost(t, Options{Identity: bid, Authorizer: NewCoordinatorAuthorizer(both), Rendezvous: rz})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// B publishes only loopback addresses, which is exactly the LAN case.
	if err := b.Publish(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}
	_, _, err := a.Connect(ctx, b.DeviceID())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Connect to a privately reachable peer returned %v; it must defer to the LAN path", err)
	}
	if got := a.Snapshot().PreferLAN; got != 1 {
		t.Fatalf("LAN preference not surfaced: %d", got)
	}
}

// TestCoordinatorRecordPairingIsRechecked: a coordinator that hands back a peer ID
// which does not derive to the requested DeviceID is refused. Otherwise a member
// could publish someone else's peer ID and redirect their traffic, and we would
// have two sources of truth about identity.
func TestCoordinatorRecordPairingIsRechecked(t *testing.T) {
	victim, _ := pool.NewIdentity()
	attacker, _ := pool.NewIdentity()
	attackerPeer, err := PeerIDFor(attacker.PublicKey)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	rz := NewStubRendezvous()
	// The stub enforces the pairing on write, which is what the coordinator must
	// also do.
	if err := rz.Publish(context.Background(), PeerRecord{
		DeviceID: pool.DeviceID(victim.PublicKey), PeerID: attackerPeer.String(),
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("mismatched record accepted on write: %v", err)
	}
	// And the client re-checks on read, so a coordinator that skipped the check
	// still cannot redirect us.
	rz.records[pool.DeviceID(victim.PublicKey)] = PeerRecord{
		DeviceID: pool.DeviceID(victim.PublicKey), PeerID: attackerPeer.String(),
	}
	a := newTestHost(t, Options{
		Authorizer: NewCoordinatorAuthorizer([]string{pool.DeviceID(victim.PublicKey)}),
		Rendezvous: rz,
	})
	if _, _, err := a.Connect(context.Background(), pool.DeviceID(victim.PublicKey)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a mismatched coordinator record was trusted: %v", err)
	}
}

func TestFingerprintShapeIsExact(t *testing.T) {
	id, _ := pool.NewIdentity()
	good := pool.DeviceID(id.PublicKey)
	for _, bad := range []string{"", good[:63], good + "0", "A" + good[1:], good[:63] + "g"} {
		if validFingerprint(bad) {
			t.Fatalf("accepted malformed fingerprint %q", bad)
		}
	}
	if !validFingerprint(good) {
		t.Fatal("rejected a real pool.DeviceID")
	}
	// Uppercase is dropped rather than folded, so an allowlist entry can never be
	// "nearly" the value peer TLS pins.
	a := NewCoordinatorAuthorizer([]string{"A" + good[1:], good})
	if len(a.Snapshot()) != 1 || !a.Authorized(good) {
		t.Fatalf("uppercase entry was normalised instead of dropped: %v", a.Snapshot())
	}
}

func mustLedger(t *testing.T, l Limits) *Ledger {
	t.Helper()
	led, err := NewLedger(l, nil)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	return led
}
