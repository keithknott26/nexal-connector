package p2p

import (
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"

	ma "github.com/multiformats/go-multiaddr"

	"nexal/connector/internal/pool"
)

// TestLANRuleIsNotWeakened imports internal/pool from the TEST binary and asserts
// that adding libp2p changed nothing about the proven transport. This follows the
// precedent internal/config set with TestStaticPeerEndpointRuleMatchesPool: the
// guard against loosening a rule is a test, not a comment.
//
// The importable direction matters — pool does NOT import p2p, so this cannot
// become a cycle, and p2p imports pool only for DeviceID.
func TestLANRuleIsNotWeakened(t *testing.T) {
	// pool.ValidPeerEndpoint must still refuse everything it refused before,
	// including every public address. If libp2p had been wired in by relaxing this,
	// these would now pass.
	for _, reject := range []string{
		"https://203.0.113.9:8443",     // public
		"https://8.8.8.8:8443",         // public
		"http://192.168.1.5:8443",      // not https
		"https://192.168.1.5",          // no explicit port
		"https://host.example:8443",    // a name, not a literal IP
		"https://u:p@192.168.1.5:8443", // credentials
		"https://192.168.1.5:8443/x",   // path
		"https://192.168.1.5:8443?q=1", // query
		"https://224.0.0.251:8443",     // multicast
		"https://0.0.0.0:8443",         // unspecified
	} {
		if err := pool.ValidPeerEndpoint(reject); err == nil {
			t.Fatalf("pool.ValidPeerEndpoint now ACCEPTS %q; the LAN transport's rule has been weakened", reject)
		}
	}
	for _, accept := range []string{
		"https://192.168.1.5:8443", "https://10.20.0.5:8443", "https://172.16.4.1:9000",
		"https://127.0.0.1:8443", "https://[fe80::1]:8443",
	} {
		if err := pool.ValidPeerEndpoint(accept); err != nil {
			t.Fatalf("pool.ValidPeerEndpoint now REJECTS %q (%v); the working LAN transport must keep working", accept, err)
		}
	}
}

// TestPrivateAddrAgreesWithTheLANRule: PrivateAddr is only a ROUTING hint (which
// transport should carry this peer), but a hint that disagreed with the LAN rule
// would send peers down the wrong path — either tunnelling a LAN peer through a
// relay, or deferring an internet peer to a transport that will refuse it.
func TestPrivateAddrAgreesWithTheLANRule(t *testing.T) {
	cases := []struct {
		multiaddr string
		endpoint  string
		private   bool
	}{
		{"/ip4/192.168.1.5/tcp/8443", "https://192.168.1.5:8443", true},
		{"/ip4/10.20.0.5/tcp/8443", "https://10.20.0.5:8443", true},
		{"/ip4/127.0.0.1/tcp/8443", "https://127.0.0.1:8443", true},
		{"/ip6/fe80::1/tcp/8443", "https://[fe80::1]:8443", true},
		{"/ip4/203.0.113.9/tcp/8443", "https://203.0.113.9:8443", false},
		{"/ip4/0.0.0.0/tcp/8443", "https://0.0.0.0:8443", false},
		{"/ip4/224.0.0.251/tcp/8443", "https://224.0.0.251:8443", false},
		{"/dns4/host.example/tcp/8443", "https://host.example:8443", false},
	}
	for _, c := range cases {
		m, err := ma.NewMultiaddr(c.multiaddr)
		if err != nil {
			t.Fatalf("multiaddr %q: %v", c.multiaddr, err)
		}
		if got := PrivateAddr(m); got != c.private {
			t.Fatalf("PrivateAddr(%q)=%v, want %v", c.multiaddr, got, c.private)
		}
		lanAccepts := pool.ValidPeerEndpoint(c.endpoint) == nil
		if lanAccepts != c.private {
			t.Fatalf("disagreement on %q: PrivateAddr says %v, pool.ValidPeerEndpoint says %v", c.multiaddr, c.private, lanAccepts)
		}
	}
	// A relayed address is never a LAN address, whatever it wraps: the point of the
	// circuit is that no direct path exists.
	relayed, err := ma.NewMultiaddr("/ip4/192.168.1.5/tcp/8443/p2p-circuit")
	if err != nil {
		t.Fatalf("multiaddr: %v", err)
	}
	if PrivateAddr(relayed) || !Relayed(relayed) {
		t.Fatal("a relayed multiaddr was classified as a private direct address")
	}
	if Relayed(nil) || PrivateAddr(nil) {
		t.Fatal("nil multiaddr misclassified")
	}
}

// TestDCUtRIsWiredAndObservable: the plan names DCUtR explicitly, so the tracer
// that makes a punch observable must actually record what libp2p reports — libp2p
// punches silently otherwise, and an unobservable transport property is one nobody
// can make decisions about (TRANSPORT-NAT-DESIGN §5).
//
// WHY THIS DOES NOT ASSERT THE /libp2p/dcutr HANDLER IS REGISTERED. libp2p's
// holepunch service deliberately waits for a PUBLIC address before registering its
// stream handler (holepunch.Service.waitForPublicAddr), and a host bound to
// loopback never has one. So in an offline test the handler is correctly absent,
// and asserting its presence would only be possible by advertising a bogus public
// address — a test that lies about the host to make itself pass. What IS asserted:
// the relay-client protocol that DCUtR runs over is registered, the tracer records
// RTT and outcomes, and the counters start clean. The punch itself is the one part
// of this package that genuinely cannot be proven without two real NATs, which
// TRANSPORT-NAT-DESIGN.md §9 records rather than papers over.
func TestDCUtRIsWiredAndObservable(t *testing.T) {
	id, _ := pool.NewIdentity()
	h := newTestHost(t, Options{Identity: id, Authorizer: NewCoordinatorAuthorizer([]string{pool.DeviceID(id.PublicKey)})})
	relayStop := false
	for _, p := range h.Libp2pHost().Mux().Protocols() {
		if p == "/libp2p/circuit/relay/0.2.0/stop" {
			relayStop = true
		}
	}
	if !relayStop {
		t.Fatalf("the relay-client protocol DCUtR signals over is not registered: %v", h.Libp2pHost().Mux().Protocols())
	}

	s := h.Snapshot().Punches
	if s.Started != 0 || s.Succeeded != 0 || s.LastRTTMillis != 0 {
		t.Fatalf("punch counters start dirty: %+v", s)
	}
	if s.Note == "" {
		t.Fatal("punch stats must say what RTT means; a bare number invites the wrong reading")
	}

	// The tracer is fed exactly the event types libp2p emits.
	tr := h.punch
	tr.Trace(&holepunch.Event{Evt: &holepunch.StartHolePunchEvt{RTT: 37 * time.Millisecond}})
	tr.Trace(&holepunch.Event{Evt: &holepunch.EndHolePunchEvt{Success: true}})
	tr.Trace(&holepunch.Event{Evt: &holepunch.StartHolePunchEvt{RTT: 120 * time.Millisecond}})
	tr.Trace(&holepunch.Event{Evt: &holepunch.EndHolePunchEvt{Success: false, Error: "no direct connection"}})
	tr.Trace(&holepunch.Event{Evt: &holepunch.DirectDialEvt{Success: true}})
	tr.Trace(nil)
	got := tr.Stats()
	if got.Started != 2 || got.Succeeded != 1 || got.Failed != 1 || got.DirectDial != 1 {
		t.Fatalf("punch events not recorded: %+v", got)
	}
	if got.LastRTTMillis != 120 {
		t.Fatalf("RTT=%d, want the most recent measurement (120)", got.LastRTTMillis)
	}
	if got.LastError != "no direct connection" {
		t.Fatalf("failure reason lost: %q", got.LastError)
	}

	// A disabled host must not build a libp2p host at all.
	off, err := New(Options{Enabled: false, Identity: id})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = off.Close() }()
	if off.Libp2pHost() != nil {
		t.Fatal("a disabled host built a libp2p host")
	}
	if err := off.Handle(nil); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled Handle: %v", err)
	}
}
