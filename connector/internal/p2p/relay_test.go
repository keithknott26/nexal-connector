package p2p

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"nexal/connector/internal/pool"
)

// TestRelayedTransferIsCappedByBytes is the HARDENING-PLAN lines 877-879 test:
// "a fallback path cannot silently run up spend". It builds a real Circuit Relay
// v2 topology in-process over loopback — relay R, sender B, receiver A — pushes
// data through it, and asserts the circuit is CUT when the owner's byte ceiling is
// reached, with the consumption surfaced.
//
// Everything is loopback and in-process: no relay on the internet is contacted,
// and the production wiring has no bootstrap list that could contact one.
func TestRelayedTransferIsCappedByBytes(t *testing.T) {
	rid, _ := pool.NewIdentity()
	aid, _ := pool.NewIdentity()
	bid, _ := pool.NewIdentity()
	all := []string{pool.DeviceID(rid.PublicKey), pool.DeviceID(aid.PublicKey), pool.DeviceID(bid.PublicKey)}
	public := network.ReachabilityPublic

	// R is the "publicly dialable donor" of HARDENING-PLAN line 901. libp2p only
	// starts the relay service once it believes it is publicly reachable, which on
	// loopback only ForceReachability can supply — see Options.ForceReachability.
	r := newTestHost(t, Options{
		Identity:          rid,
		Authorizer:        NewCoordinatorAuthorizer(all),
		Limits:            Limits{OfferRelayService: true},
		ForceReachability: &public,
	})
	if !r.Snapshot().RelayService {
		t.Fatal("relay service was not offered")
	}

	rz := NewStubRendezvous()
	a := newTestHost(t, Options{Identity: aid, Authorizer: NewCoordinatorAuthorizer(all), Rendezvous: rz})
	// B's ceiling is tiny so the cap is reached in a test-sized transfer. The
	// ceiling being owner-configurable is the point: this is the same field an
	// owner sets in config.p2p.maxCircuitBytes.
	b := newTestHost(t, Options{
		Identity: bid, Authorizer: NewCoordinatorAuthorizer(all), Rendezvous: rz,
		Limits: Limits{MaxCircuitBytes: 8 << 10, MaxTotalBytes: 1 << 20, MaxRelayedConns: 2},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := a.Reserve(ctx, selfInfo(r)); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if got := a.Snapshot().Reservations; got != 1 {
		t.Fatalf("reservations=%d", got)
	}

	received := make(chan int, 1)
	if err := a.Handle(func(s network.Stream) {
		total := 0
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			total += n
			if err != nil {
				break
			}
		}
		received <- total
		_ = s.Reset()
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Publish ONLY the relayed address for A, so B has no direct path and the
	// relay is genuinely exercised rather than shadowed by a loopback dial.
	circuit := r.Addrs()[0].String() + "/p2p/" + r.PeerID().String() + "/p2p-circuit"
	if err := rz.Publish(ctx, PeerRecord{
		DeviceID: a.DeviceID(), PeerID: a.PeerID().String(), Addrs: []string{circuit},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// B has to know how to reach the relay itself before it can dial through it;
	// in production GET /v1/relays supplies this.
	b.Libp2pHost().Peerstore().AddAddrs(r.PeerID(), r.Addrs(), time.Hour)

	id, relayed, err := b.Connect(ctx, a.DeviceID())
	if err != nil {
		t.Fatalf("relayed connect: %v", err)
	}
	if id != a.PeerID() || !relayed {
		t.Fatalf("expected a relayed connection to A, got %v relayed=%v", id, relayed)
	}

	s, err := b.OpenStream(ctx, a.PeerID())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	metered, ok := s.(*MeteredStream)
	if !ok {
		t.Fatal("a relayed stream was NOT metered; the byte ceiling would not apply and spend would be unbounded")
	}

	// Push well past the 8 KiB ceiling.
	chunk := make([]byte, 1024)
	var written int
	var budgetErr error
	for i := 0; i < 64; i++ {
		n, err := metered.Write(chunk)
		written += n
		if err != nil {
			budgetErr = err
			break
		}
	}
	if !errors.Is(budgetErr, ErrBudget) {
		t.Fatalf("wrote %d bytes over a relay with an 8 KiB ceiling and got %v; an unbounded relay path is a failed implementation of HARDENING-PLAN 877-879", written, budgetErr)
	}
	if written > 16<<10 {
		t.Fatalf("ceiling overshot badly: %d bytes went out under an 8 KiB cap", written)
	}

	c := b.Consumption()
	if c.RelayBytesUsed == 0 {
		t.Fatal("relay consumption was not surfaced; an unobservable cap is not a control")
	}
	if c.CircuitsTruncated == 0 {
		t.Fatal("the truncation was not counted, so an owner cannot tell a capped transfer from a network failure")
	}
	if c.RelayedCircuitsEver != 1 {
		t.Fatalf("circuits booked=%d, want 1", c.RelayedCircuitsEver)
	}
	if c.Limits.MaxCircuitBytes != 8<<10 {
		t.Fatalf("owner ceiling not honoured: %+v", c.Limits)
	}
	if c.Note == "" {
		t.Fatal("Consumption must state what the counters do and do not cover")
	}
	// The receiver saw some bytes but not all of them, which is the whole point: a
	// capped circuit fails the transfer rather than silently paying for it.
	select {
	case got := <-received:
		if got > 16<<10 {
			t.Fatalf("receiver got %d bytes past an 8 KiB ceiling", got)
		}
	case <-time.After(3 * time.Second):
		// Acceptable: the reset may leave the peer blocked on a read, and the
		// assertion under test is B's refusal to keep spending, not A's bookkeeping.
	}
}

// TestRelayReservationRequiresAnAuthorizedRelay: a relay sees connection metadata
// for every circuit it carries, so reserving on an unauthorized host would hand
// traffic analysis to a stranger and let an outsider into the path.
func TestRelayReservationRequiresAnAuthorizedRelay(t *testing.T) {
	rid, _ := pool.NewIdentity()
	public := network.ReachabilityPublic
	r := newTestHost(t, Options{
		Identity: rid, Authorizer: NewCoordinatorAuthorizer([]string{pool.DeviceID(rid.PublicKey)}),
		Limits: Limits{OfferRelayService: true}, ForceReachability: &public,
	})
	// A does not authorize R.
	a := newTestHost(t, Options{Authorizer: NewCoordinatorAuthorizer(nil)})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Reserve(ctx, selfInfo(r)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("reserved on an unauthorized relay: %v", err)
	}
	if got := a.Snapshot().Reservations; got != 0 {
		t.Fatalf("reservations=%d after a refused reserve", got)
	}
	// A malformed peer ID is refused by the identity mapping, not by a dial
	// attempt: there is no DeviceID to check, so there is nothing to authorize.
	if err := a.Reserve(ctx, peer.AddrInfo{ID: peer.ID("not-a-peer")}); err == nil {
		t.Fatal("reserved on a relay whose peer ID carries no derivable fingerprint")
	}
}
