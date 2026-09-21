package stun

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// EVERY TEST IN THIS FILE IS OFFLINE. The only socket any of them opens is a
// loopback UDP listener in this process (TestObserveAgainstLoopbackResponder);
// nothing resolves or contacts a real STUN server, because a test that needs
// stun.cloudflare.com is a test that fails in CI with no network and teaches us
// nothing about the parser.

// fakeExchanger answers from a table. It records the local "socket" identity by
// counting how many times it was constructed, which is how
// TestObserveUsesOneSocket asserts the single-socket property.
type fakeExchanger struct {
	respond func(server string, request []byte) ([]byte, error)
	closed  bool
	calls   []string
}

func (f *fakeExchanger) Exchange(ctx context.Context, server string, request []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("exchange must be deadline bounded")
	}
	f.calls = append(f.calls, server)
	return f.respond(server, request)
}

func (f *fakeExchanger) Close() error { f.closed = true; return nil }

func respondWith(addrs map[string]netip.AddrPort, xor bool) func(string, []byte) ([]byte, error) {
	return func(server string, request []byte) ([]byte, error) {
		txid, ok := RequestTransactionID(request)
		if !ok {
			return nil, errors.New("not a binding request")
		}
		addr, known := addrs[server]
		if !known {
			return nil, errors.New("no such server")
		}
		return EncodeBindingSuccess(txid, addr, xor), nil
	}
}

func TestBindingRequestShape(t *testing.T) {
	msg, txid, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) != headerBytes {
		t.Fatalf("request is %d bytes, want %d", len(msg), headerBytes)
	}
	if binary.BigEndian.Uint16(msg[0:2]) != bindingRequest {
		t.Fatal("wrong message type")
	}
	if binary.BigEndian.Uint16(msg[2:4]) != 0 {
		t.Fatal("request must declare zero attribute bytes")
	}
	if binary.BigEndian.Uint32(msg[4:8]) != magicCookie {
		t.Fatal("wrong magic cookie")
	}
	// Two requests must not share a transaction id; a repeated id would make a
	// captured response replayable.
	_, second, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	if txid == second {
		t.Fatal("transaction ids repeated")
	}
	var zero TransactionID
	if txid == zero {
		t.Fatal("transaction id is zero; it must be random")
	}
}

func TestParseXORMappedIPv4AndIPv6(t *testing.T) {
	for _, want := range []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.9:51820"),
		netip.MustParseAddrPort("[2001:db8::1]:51820"),
	} {
		_, txid, err := NewBindingRequest()
		if err != nil {
			t.Fatal(err)
		}
		got, legacy, err := Parse(EncodeBindingSuccess(txid, want, true), txid)
		if err != nil {
			t.Fatalf("%v: %v", want, err)
		}
		if got != want {
			t.Fatalf("parsed %v, want %v", got, want)
		}
		if legacy {
			t.Fatal("XOR-MAPPED-ADDRESS must not be reported as legacy")
		}
	}
}

func TestParseLegacyMappedAddressAcceptedAsFallback(t *testing.T) {
	want := netip.MustParseAddrPort("198.51.100.7:3478")
	_, txid, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	got, legacy, err := Parse(EncodeBindingSuccess(txid, want, false), txid)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !legacy {
		t.Fatalf("got %v legacy=%v, want %v legacy=true", got, legacy, want)
	}
}

// A message carrying both forms must yield the XOR one: it is the form a NAT
// ALG cannot rewrite behind our back.
func TestXORMappedAddressWinsOverLegacy(t *testing.T) {
	_, txid, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	legacyMsg := EncodeBindingSuccess(txid, netip.MustParseAddrPort("10.0.0.1:1"), false)
	xorMsg := EncodeBindingSuccess(txid, netip.MustParseAddrPort("203.0.113.9:443"), true)
	// Splice: header from the legacy message, both attribute blocks in the body,
	// legacy first so the parser has to prefer the later XOR attribute.
	body := append(append([]byte{}, legacyMsg[headerBytes:]...), xorMsg[headerBytes:]...)
	msg := append([]byte{}, legacyMsg[:headerBytes]...)
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(body)))
	msg = append(msg, body...)
	got, legacy, err := Parse(msg, txid)
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParseAddrPort("203.0.113.9:443") || legacy {
		t.Fatalf("got %v legacy=%v; XOR form must win", got, legacy)
	}
}

// The single most important test in this package: a reply that does not carry
// our transaction id must never produce an address, however well-formed it is.
func TestWrongTransactionIDIsNeverTrusted(t *testing.T) {
	_, mine, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	_, theirs, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	spoof := EncodeBindingSuccess(theirs, netip.MustParseAddrPort("192.0.2.66:1234"), true)
	got, _, err := Parse(spoof, mine)
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
	if got.IsValid() {
		t.Fatal("an unmatched response must yield no address")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	_, txid, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	good := EncodeBindingSuccess(txid, netip.MustParseAddrPort("203.0.113.9:4242"), true)
	mutate := func(fn func([]byte) []byte) []byte { return fn(append([]byte{}, good...)) }

	cases := []struct {
		name string
		msg  []byte
		want error
	}{
		{"empty", nil, ErrMalformed},
		{"header only truncated", good[:headerBytes-1], ErrMalformed},
		{"body truncated but header claims full length", good[:len(good)-4], ErrMalformed},
		{"oversized", make([]byte, maxMessageBytes+1), ErrMalformed},
		{"wrong cookie", mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[4:8], 0xdeadbeef)
			return b
		}), ErrMalformed},
		{"leading bits set (not stun)", mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[0:2], 0xC101)
			return b
		}), ErrMalformed},
		{"unaligned length", mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[2:4], 6)
			return b
		}), ErrMalformed},
		{"attribute length overruns body", mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[headerBytes+2:headerBytes+4], 0xffff)
			return b
		}), ErrMalformed},
		{"unknown address family", mutate(func(b []byte) []byte {
			b[headerBytes+5] = 0x09
			return b
		}), ErrMalformed},
		{"truncated ipv4 value", func() []byte {
			attr := binary.BigEndian.AppendUint16(nil, attrXORMappedAddress)
			attr = binary.BigEndian.AppendUint16(attr, 4)
			attr = append(attr, 0, familyIPv4, 0x11, 0x22) // family + port, no address
			msg := binary.BigEndian.AppendUint16(nil, bindingSuccess)
			msg = binary.BigEndian.AppendUint16(msg, uint16(len(attr)))
			msg = binary.BigEndian.AppendUint32(msg, magicCookie)
			msg = append(msg, txid[:]...)
			return append(msg, attr...)
		}(), ErrMalformed},
		{"error response", func() []byte {
			b := append([]byte{}, good...)
			binary.BigEndian.PutUint16(b[0:2], bindingErrorResponse)
			return b
		}(), ErrServerError},
		{"success with no address attribute", func() []byte {
			msg := binary.BigEndian.AppendUint16(nil, bindingSuccess)
			msg = binary.BigEndian.AppendUint16(msg, 0)
			msg = binary.BigEndian.AppendUint32(msg, magicCookie)
			return append(msg, txid[:]...)
		}(), ErrNoAddress},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, _, err := Parse(c.msg, txid)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if addr.IsValid() {
				t.Fatal("a rejected message must yield no address")
			}
		})
	}
}

// Unknown comprehension-optional attributes must be skipped, and ALTERNATE-SERVER
// must be ignored rather than followed.
func TestUnknownAndAlternateServerAttributesAreSkipped(t *testing.T) {
	want := netip.MustParseAddrPort("203.0.113.9:9999")
	_, txid, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	unknown := binary.BigEndian.AppendUint16(nil, 0x8022) // SOFTWARE
	unknown = binary.BigEndian.AppendUint16(unknown, 5)
	unknown = append(unknown, 'a', 'b', 'c', 'd', 'e', 0, 0, 0) // padded to 8

	alternate := binary.BigEndian.AppendUint16(nil, attrAlternateServer)
	alternate = binary.BigEndian.AppendUint16(alternate, 8)
	alternate = append(alternate, 0, familyIPv4, 0x0d, 0x96, 192, 0, 2, 1)

	xorAttr := EncodeBindingSuccess(txid, want, true)[headerBytes:]
	body := append(append(append([]byte{}, unknown...), alternate...), xorAttr...)
	msg := binary.BigEndian.AppendUint16(nil, bindingSuccess)
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(body)))
	msg = binary.BigEndian.AppendUint32(msg, magicCookie)
	msg = append(msg, txid[:]...)
	msg = append(msg, body...)

	got, _, err := Parse(msg, txid)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %v, want %v; the address attribute must survive unknown ones", got, want)
	}
}

func TestClassificationIsTriState(t *testing.T) {
	a := netip.MustParseAddrPort("203.0.113.9:1111")
	b := netip.MustParseAddrPort("203.0.113.9:2222")
	cases := []struct {
		name string
		seen []netip.AddrPort
		want Mapping
	}{
		{"no samples", nil, MappingUnknown},
		{"one sample is never a classification", []netip.AddrPort{a}, MappingUnknown},
		{"same address from two servers", []netip.AddrPort{a, a}, MappingEndpointIndependent},
		{"different port per destination", []netip.AddrPort{a, b}, MappingEndpointDependent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.seen); got != c.want {
				t.Fatalf("classify = %v, want %v", got, c.want)
			}
		})
	}
	// The honesty requirement, asserted rather than commented: nothing in this
	// package may state that hole punching WILL work.
	for _, m := range []Mapping{MappingUnknown, MappingEndpointIndependent, MappingEndpointDependent} {
		if strings.Contains(m.Summary(), "will work") {
			t.Fatalf("%v summary promises a working hole punch: %q", m, m.Summary())
		}
	}
}

func TestObserveEndpointIndependent(t *testing.T) {
	addr := netip.MustParseAddrPort("203.0.113.9:40000")
	fake := &fakeExchanger{respond: respondWith(map[string]netip.AddrPort{
		"a.example:3478": addr, "b.example:3478": addr}, true)}
	c := &Client{Servers: []string{"a.example:3478", "b.example:3478"},
		NewExchanger: func() (Exchanger, error) { return fake, nil }}
	result, err := c.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || result.Reflexive != addr {
		t.Fatalf("result = %+v", result)
	}
	if result.Mapping != MappingEndpointIndependent || result.MappingLabel != "endpoint-independent" {
		t.Fatalf("mapping = %v/%q", result.Mapping, result.MappingLabel)
	}
	if !fake.closed {
		t.Fatal("exchanger must be closed")
	}
}

func TestObserveEndpointDependent(t *testing.T) {
	fake := &fakeExchanger{respond: respondWith(map[string]netip.AddrPort{
		"a.example:3478": netip.MustParseAddrPort("203.0.113.9:1000"),
		"b.example:3478": netip.MustParseAddrPort("203.0.113.9:2000")}, true)}
	c := &Client{Servers: []string{"a.example:3478", "b.example:3478"},
		NewExchanger: func() (Exchanger, error) { return fake, nil }}
	result, err := c.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Mapping != MappingEndpointDependent {
		t.Fatalf("mapping = %v, want endpoint-dependent", result.Mapping)
	}
	if !strings.Contains(result.Summary, "relay") {
		t.Fatalf("summary should say a relay would be needed: %q", result.Summary)
	}
}

// One server answering is NOT a classification, and one server failing is not an
// error: an unreachable STUN path is a legitimate, reportable observation.
func TestObserveSingleAnswerStaysUnknownAndFailureIsNotAnError(t *testing.T) {
	fake := &fakeExchanger{respond: respondWith(map[string]netip.AddrPort{
		"a.example:3478": netip.MustParseAddrPort("203.0.113.9:1000")}, true)}
	c := &Client{Servers: []string{"a.example:3478", "down.example:3478"},
		NewExchanger: func() (Exchanger, error) { return fake, nil }}
	result, err := c.Observe(context.Background())
	if err != nil {
		t.Fatalf("a failed server must not fail Observe: %v", err)
	}
	if !result.Reachable || result.Mapping != MappingUnknown {
		t.Fatalf("result = %+v, want reachable with unknown mapping", result)
	}
	if result.Observations[1].Error == "" {
		t.Fatal("the failed server must carry a reason")
	}
}

func TestObserveAllServersUnreachable(t *testing.T) {
	fake := &fakeExchanger{respond: func(string, []byte) ([]byte, error) {
		return nil, errors.New("udp blocked")
	}}
	c := &Client{Servers: []string{"a.example:3478", "b.example:3478"},
		NewExchanger: func() (Exchanger, error) { return fake, nil }}
	result, err := c.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reachable || result.Reflexive.IsValid() || result.Mapping != MappingUnknown {
		t.Fatalf("result = %+v, want unreachable/unknown", result)
	}
}

// Spoofed datagrams on the socket must be skipped, counted and not believed,
// while the genuine reply that follows is still accepted.
func TestObserveSkipsSpoofedResponses(t *testing.T) {
	_, other, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	want := netip.MustParseAddrPort("203.0.113.9:7000")
	attempts := 0
	fake := &fakeExchanger{respond: func(server string, request []byte) ([]byte, error) {
		attempts++
		if attempts == 1 {
			return EncodeBindingSuccess(other, netip.MustParseAddrPort("192.0.2.1:1"), true), nil
		}
		txid, _ := RequestTransactionID(request)
		return EncodeBindingSuccess(txid, want, true), nil
	}}
	c := &Client{Servers: []string{"a.example:3478"},
		NewExchanger: func() (Exchanger, error) { return fake, nil }}
	result, err := c.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reflexive != want {
		t.Fatalf("reflexive = %v, want %v", result.Reflexive, want)
	}
	if result.Observations[0].Unmatched != 1 {
		t.Fatalf("unmatched = %d, want 1", result.Observations[0].Unmatched)
	}
}

// The classification is only meaningful if both queries leave from ONE local
// port, so Observe must construct exactly one exchanger however many servers it
// queries.
func TestObserveUsesOneSocket(t *testing.T) {
	built := 0
	fake := &fakeExchanger{respond: respondWith(map[string]netip.AddrPort{
		"a.example:3478": netip.MustParseAddrPort("203.0.113.9:1"),
		"b.example:3478": netip.MustParseAddrPort("203.0.113.9:1")}, true)}
	c := &Client{Servers: []string{"a.example:3478", "b.example:3478"},
		NewExchanger: func() (Exchanger, error) { built++; return fake, nil }}
	if _, err := c.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if built != 1 {
		t.Fatalf("built %d exchangers, want 1 (one local port, or the mapping comparison is meaningless)", built)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("queried %v, want both servers", fake.calls)
	}
}

func TestObserveRejectsBadConfiguration(t *testing.T) {
	c := &Client{Servers: []string{"missing-port"}}
	if _, err := c.Observe(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	many := make([]string, maxServers+1)
	for i := range many {
		many[i] = "a.example:3478"
	}
	if _, err := (&Client{Servers: many}).Observe(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for too many servers", err)
	}
}

func TestObserveHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeExchanger{respond: respondWith(map[string]netip.AddrPort{}, true)}
	c := &Client{Servers: []string{"a.example:3478", "b.example:3478"},
		NewExchanger: func() (Exchanger, error) { return fake, nil }}
	result, err := c.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reachable || len(result.Observations) != 1 {
		t.Fatalf("a cancelled observe must stop after the first server: %+v", result)
	}
}

// The real UDP exchanger, against a responder in this process on loopback. This
// is the only test that opens a socket, and it never leaves the machine.
func TestObserveAgainstLoopbackResponder(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("loopback UDP unavailable in this sandbox: %v", err)
	}
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		for {
			n, from, err := server.ReadFromUDP(buf)
			if err != nil {
				return
			}
			txid, ok := RequestTransactionID(buf[:n])
			if !ok {
				continue
			}
			// Answer with the sender's own address, which is what a STUN server
			// does, and which here is a loopback address.
			addr, _ := netip.AddrFromSlice(from.IP.To4())
			reply := EncodeBindingSuccess(txid, netip.AddrPortFrom(addr, uint16(from.Port)), true)
			if _, err := server.WriteToUDP(reply, from); err != nil {
				return
			}
		}
	}()

	target := server.LocalAddr().(*net.UDPAddr)
	addr := net.JoinHostPort("127.0.0.1", itoa(target.Port))
	c := &Client{Servers: []string{addr, addr}, PerServerTimeout: 3 * time.Second}
	result, err := c.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable {
		t.Fatalf("loopback responder unreachable: %+v", result)
	}
	if !result.Reflexive.Addr().IsLoopback() {
		t.Fatalf("reflexive = %v, want a loopback address from the local responder", result.Reflexive)
	}
	// Both queries left from one socket, so the same port must come back twice.
	if result.Mapping != MappingEndpointIndependent {
		t.Fatalf("mapping = %v; one socket to one responder must look endpoint-independent", result.Mapping)
	}
	server.Close()
	<-done
}

// A deadline-free context must be refused by the UDP exchanger rather than
// blocking forever on a read.
func TestUDPExchangerRequiresDeadline(t *testing.T) {
	e, err := NewUDPExchanger()
	if err != nil {
		t.Skipf("UDP socket unavailable in this sandbox: %v", err)
	}
	defer e.Close()
	request, _, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Exchange(context.Background(), "127.0.0.1:3478", request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for an unbounded context", err)
	}
}

func TestDefaultServersAreTwoDistinctFreeHosts(t *testing.T) {
	if len(DefaultServers) < 2 {
		t.Fatal("at least two servers are required or the mapping is always unknown")
	}
	hosts := map[string]bool{}
	for _, s := range DefaultServers {
		host, _, err := net.SplitHostPort(s)
		if err != nil {
			t.Fatalf("%q is not host:port", s)
		}
		if hosts[host] {
			t.Fatalf("%q repeats a host; two ports on one host only reveal port-dependent mapping", s)
		}
		hosts[host] = true
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
