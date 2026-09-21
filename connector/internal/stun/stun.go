// Package stun is a hand-rolled, zero-dependency RFC 5389 binding client.
//
// WHAT THIS IS FOR, precisely, because the name invites over-reading: this
// package answers "what address does the internet see me as, and does my NAT
// keep that address stable across destinations". That is OBSERVABILITY. It
// establishes no connection and moves no payload bytes.
//
// STUN ALONE ESTABLISHES NOTHING. A reflexive address is useless without (a) a
// coordinator that signals it to the other peer and (b) both peers dialling each
// other simultaneously so each NAT sees an outbound packet before the inbound
// one arrives. Neither exists in this connector. See TRANSPORT-NAT-DESIGN.md:
// this is Phase 1, hole punching is Phase 2 and is not started.
//
// Why hand-rolled rather than pion/stun: the binding request is 20 bytes and the
// only reply attribute we need is XOR-MAPPED-ADDRESS. The house rule is zero new
// dependencies, and the whole wire format fits in this file with room for the
// reasoning.
//
// SECURITY PROPERTIES, each of which is a deliberate refusal:
//   - The 96-bit transaction ID comes from crypto/rand and every response is
//     matched against it before a single byte is trusted. UDP is trivially
//     spoofable by an off-path attacker who can guess a port; the transaction ID
//     is the only thing that makes a reply attributable. An unmatched datagram is
//     counted and discarded, never parsed for an address.
//   - ALTERNATE-SERVER (0x8023) and any other redirect are IGNORED. Following a
//     server-chosen redirect would let one answer point this host at an arbitrary
//     UDP endpoint.
//   - Reads are bounded (maxMessageBytes) and every attribute walk is
//     bounds-checked against the declared header length, so a truncated or lying
//     packet ends the parse instead of indexing past the buffer.
//   - Every exchange is context-bounded and the socket carries a hard deadline.
package stun

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	// headerBytes is the fixed RFC 5389 §6 header: type, length, cookie, txid.
	headerBytes = 20
	// magicCookie identifies STUN and is the key half of the XOR in
	// XOR-MAPPED-ADDRESS (RFC 5389 §6).
	magicCookie uint32 = 0x2112A442
	// transactionIDBytes is 96 bits, per RFC 5389 §6, and is the only
	// anti-spoofing material a binding exchange has.
	transactionIDBytes = 12

	// methodBinding plus the class bits give the two message types we handle.
	bindingRequest       uint16 = 0x0001
	bindingSuccess       uint16 = 0x0101
	bindingErrorResponse uint16 = 0x0111

	attrMappedAddress    uint16 = 0x0001
	attrXORMappedAddress uint16 = 0x0020
	attrErrorCode        uint16 = 0x0009
	// attrAlternateServer is parsed only so the constant documents that we
	// deliberately do nothing with it. See the package comment.
	attrAlternateServer uint16 = 0x8023

	familyIPv4 uint8 = 0x01
	familyIPv6 uint8 = 0x02

	// maxMessageBytes bounds a single read. A binding response is tens of bytes;
	// anything near a full MTU is either not ours or is trying to make us
	// allocate. Bounded reads are the rule the mDNS decoder already follows.
	maxMessageBytes = 1280
	// maxDatagramsPerExchange bounds how many unmatched datagrams we will skip
	// past while waiting for our own reply, so a flood cannot hold a goroutine.
	maxDatagramsPerExchange = 8
)

// Default servers. Two DISTINCT hosts, on purpose: comparing the reflexive
// address seen by two different server addresses is what distinguishes an
// endpoint-independent mapping (hole punching has a chance) from an
// endpoint-dependent/symmetric one (it does not). Two ports on one host would
// only reveal port-dependent behaviour.
//
// Both are free: Cloudflare documents STUN as "free and unlimited" and documents
// that turn.cloudflare.com also answers binding requests
// (https://developers.cloudflare.com/realtime/turn/faq/). STUN reveals only our
// own reflexive address and relays nothing, which is why it is acceptable here
// while Cloudflare TURN is rejected in TRANSPORT-NAT-DESIGN.md.
var DefaultServers = []string{"stun.cloudflare.com:3478", "turn.cloudflare.com:3478"}

var (
	ErrInvalid   = errors.New("stun: invalid configuration")
	ErrMalformed = errors.New("stun: malformed response")
	// ErrNoMatch means nothing arrived carrying our transaction ID. It is
	// distinct from a timeout because the difference matters for diagnosis: a
	// blocked UDP path and an off-path spoofer are not the same fault.
	ErrNoMatch      = errors.New("stun: no response matched the transaction id")
	ErrNoAddress    = errors.New("stun: response carried no mapped address")
	ErrServerError  = errors.New("stun: server returned an error response")
	ErrResponseSize = errors.New("stun: response exceeds the bounded read size")
)

// Mapping is the NAT behaviour classification, and it is deliberately a
// TRI-STATE. "Unknown" is a first-class answer, following the convention
// internal/contribution and internal/throttle already use for an unreadable
// platform signal: one sample, or a failed sample, must never be reported as
// "hole punching will work". It would not be a measurement, it would be a guess
// that later turns into a support incident.
type Mapping uint8

const (
	MappingUnknown Mapping = iota
	// MappingEndpointIndependent: both servers saw the same reflexive address
	// and port, so the NAT reuses one mapping regardless of destination. Hole
	// punching is PLAUSIBLE — still not proven, because only a real
	// simultaneous dial proves it.
	MappingEndpointIndependent
	// MappingEndpointDependent: the reflexive address or port differed per
	// destination (symmetric NAT, or CGNAT behaving that way). Hole punching
	// will not work; that peer pair needs a relay.
	MappingEndpointDependent
)

func (m Mapping) String() string {
	switch m {
	case MappingEndpointIndependent:
		return "endpoint-independent"
	case MappingEndpointDependent:
		return "endpoint-dependent"
	}
	return "unknown"
}

// Summary is the honest sentence a diagnostic surface should print. It never
// promises a working hole punch, because this package cannot establish one.
func (m Mapping) Summary() string {
	switch m {
	case MappingEndpointIndependent:
		return "NAT reused one mapping for two different servers (endpoint-independent); hole punching is plausible but unproven — nothing here punches a hole"
	case MappingEndpointDependent:
		return "NAT used a different mapping per destination (endpoint-dependent/symmetric); hole punching between two such peers will fail and would need a relay"
	}
	return "NAT mapping behaviour is unknown: fewer than two servers answered, so no comparison was possible"
}

// Observation is one server's answer, kept individually so a diagnostic can show
// which server failed rather than collapsing everything into one boolean.
type Observation struct {
	Server    string         `json:"server"`
	Reflexive netip.AddrPort `json:"reflexive,omitzero"`
	// Legacy is true when the address came from MAPPED-ADDRESS rather than
	// XOR-MAPPED-ADDRESS. Worth surfacing: a NAT that rewrites payload IPs
	// ("ALG") corrupts the legacy form, which is exactly why the XOR form exists.
	Legacy bool `json:"legacy"`
	// Unmatched counts datagrams that arrived on our socket without our
	// transaction ID. Nonzero is worth seeing: that is either unrelated traffic
	// or an off-path spoof attempt.
	Unmatched int    `json:"unmatched"`
	Error     string `json:"error,omitempty"`
}

// Result is the whole observation, including the negative case.
type Result struct {
	// Reachable is true if at least one server answered with an address. False
	// means UDP 3478 is blocked, or DNS failed, or we are offline — the report
	// says which through the per-observation errors.
	Reachable    bool           `json:"reachable"`
	Reflexive    netip.AddrPort `json:"reflexive,omitzero"`
	Mapping      Mapping        `json:"-"`
	MappingLabel string         `json:"mapping"`
	Summary      string         `json:"summary"`
	Observations []Observation  `json:"observations"`
}

// Exchanger sends one request to one server and returns the raw response.
//
// It exists so every test in this package runs offline: the default
// implementation is a UDP socket, and tests inject either a fixture responder or
// a loopback listener. No test contacts a real STUN server.
//
// ONE EXCHANGER MUST USE ONE LOCAL SOCKET for the life of an Observe call. This
// is not an optimisation, it is the correctness condition of the whole
// classification: a NAT mapping is keyed on the LOCAL port, so comparing the
// reflexive address of two queries sent from two different sockets would report
// "endpoint-dependent" on every NAT on earth, including the most permissive.
type Exchanger interface {
	Exchange(ctx context.Context, server string, request []byte) ([]byte, error)
	Close() error
}

// udpExchanger holds one unconnected UDP socket and writes to each server from
// it, preserving the single-local-port property described on Exchanger.
type udpExchanger struct {
	conn     *net.UDPConn
	resolver *net.Resolver
}

// NewUDPExchanger binds one ephemeral UDP socket on all interfaces.
//
// Unlike the peer transport in internal/pool, DNS is required here and that is
// fine: a STUN server is a public name, the reply is only an address
// observation, and the transaction ID (not the destination) is what makes the
// answer trustworthy. The peer transport's numeric-private-IP-only rule is
// untouched by this package.
func NewUDPExchanger() (Exchanger, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	return &udpExchanger{conn: conn, resolver: net.DefaultResolver}, nil
}

func (u *udpExchanger) Close() error { return u.conn.Close() }

func (u *udpExchanger) Exchange(ctx context.Context, server string, request []byte) ([]byte, error) {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return nil, ErrInvalid
	}
	addrs, err := u.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("stun: cannot resolve server: %w", err)
	}
	p, err := net.LookupPort("udp", port)
	if err != nil || p < 1 || p > 65535 {
		return nil, ErrInvalid
	}
	// One destination, the first resolved address. Trying every address would
	// multiply the sample count without improving the comparison, and the caller
	// already queries two distinct servers.
	target := net.UDPAddrFromAddrPort(netip.AddrPortFrom(addrs[0].Unmap(), uint16(p)))
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, ErrInvalid // an unbounded UDP read is how a CLI hangs forever
	}
	if err := u.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if _, err := u.conn.WriteToUDP(request, target); err != nil {
		return nil, err
	}
	// Read past datagrams that are not ours. The caller matches the transaction
	// id; this loop only refuses to give up on the first stranger, because on a
	// busy socket the first datagram back may be unrelated.
	buf := make([]byte, maxMessageBytes+1)
	for range maxDatagramsPerExchange {
		n, from, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		if n > maxMessageBytes {
			return nil, ErrResponseSize
		}
		// Source-address filtering is a cheap first cut only. It is NOT the
		// security property — address spoofing defeats it, which is why the
		// transaction id check in Parse is the one that matters.
		if !from.IP.Equal(target.IP) {
			continue
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		return out, nil
	}
	return nil, ErrNoMatch
}

// Client queries servers and classifies the result.
type Client struct {
	// Servers is the query list; empty means DefaultServers. At least two
	// distinct servers are needed for any mapping classification other than
	// unknown.
	Servers []string
	// NewExchanger is injectable so tests never touch the network; nil means
	// NewUDPExchanger.
	NewExchanger func() (Exchanger, error)
	// PerServerTimeout bounds one server's exchange. Zero means
	// DefaultPerServerTimeout.
	PerServerTimeout time.Duration
}

// DefaultPerServerTimeout is short on purpose: this runs inside `nexal doctor`,
// where a slow diagnostic is a diagnostic nobody waits for, and a STUN server
// that needs more than two seconds is not a usable one anyway.
const DefaultPerServerTimeout = 2 * time.Second

const maxServers = 8

// Observe queries each configured server from ONE local socket and classifies
// the mapping. It never returns an error for "no server answered": an
// unreachable STUN path is a legitimate observation and is reported as
// Reachable:false, because a diagnostic that errors out tells the owner less
// than one that says "UDP 3478 appears blocked".
func (c *Client) Observe(ctx context.Context) (Result, error) {
	servers := c.Servers
	if len(servers) == 0 {
		servers = DefaultServers
	}
	if len(servers) > maxServers {
		return Result{}, ErrInvalid
	}
	for _, s := range servers {
		if _, _, err := net.SplitHostPort(s); err != nil {
			return Result{}, ErrInvalid
		}
	}
	newExchanger := c.NewExchanger
	if newExchanger == nil {
		newExchanger = NewUDPExchanger
	}
	exchanger, err := newExchanger()
	if err != nil {
		return Result{}, err
	}
	defer exchanger.Close()

	timeout := c.PerServerTimeout
	if timeout <= 0 {
		timeout = DefaultPerServerTimeout
	}
	result := Result{Observations: make([]Observation, 0, len(servers))}
	var seen []netip.AddrPort
	for _, server := range servers {
		observation := Observation{Server: server}
		addr, legacy, unmatched, err := c.query(ctx, exchanger, server, timeout)
		observation.Unmatched = unmatched
		switch {
		case err != nil:
			observation.Error = err.Error()
		default:
			observation.Reflexive, observation.Legacy = addr, legacy
			seen = append(seen, addr)
			if !result.Reachable {
				result.Reachable, result.Reflexive = true, addr
			}
		}
		result.Observations = append(result.Observations, observation)
		if ctx.Err() != nil {
			break // cancelled: report what we have rather than inventing the rest
		}
	}
	result.Mapping = classify(seen)
	result.MappingLabel, result.Summary = result.Mapping.String(), result.Mapping.Summary()
	return result, nil
}

// classify is the whole NAT judgement, and the conservative direction is
// deliberate: fewer than two samples is UNKNOWN, never "independent".
func classify(seen []netip.AddrPort) Mapping {
	if len(seen) < 2 {
		return MappingUnknown
	}
	for _, addr := range seen[1:] {
		if addr != seen[0] {
			return MappingEndpointDependent
		}
	}
	return MappingEndpointIndependent
}

func (c *Client) query(ctx context.Context, e Exchanger, server string, timeout time.Duration) (netip.AddrPort, bool, int, error) {
	request, txid, err := NewBindingRequest()
	if err != nil {
		return netip.AddrPort{}, false, 0, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	unmatched := 0
	for range maxDatagramsPerExchange {
		response, err := e.Exchange(queryCtx, server, request)
		if err != nil {
			return netip.AddrPort{}, false, unmatched, err
		}
		addr, legacy, err := Parse(response, txid)
		if errors.Is(err, ErrNoMatch) {
			// Someone else's datagram, or an off-path attacker guessing. Neither
			// is an answer: keep waiting inside the same deadline.
			unmatched++
			continue
		}
		if err != nil {
			return netip.AddrPort{}, false, unmatched, err
		}
		return addr, legacy, unmatched, nil
	}
	return netip.AddrPort{}, false, unmatched, ErrNoMatch
}

// TransactionID is the 96-bit per-request nonce.
type TransactionID [transactionIDBytes]byte

// NewBindingRequest builds a 20-byte binding request with no attributes and a
// cryptographically random transaction id. math/rand would make the id
// predictable, and a predictable id is a forgeable response.
func NewBindingRequest() ([]byte, TransactionID, error) {
	var txid TransactionID
	if _, err := rand.Read(txid[:]); err != nil {
		return nil, txid, err
	}
	msg := make([]byte, headerBytes)
	binary.BigEndian.PutUint16(msg[0:2], bindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], txid[:])
	return msg, txid, nil
}

// Parse validates a response against the expected transaction id and returns the
// reflexive address.
//
// Order of checks is the security order: shape, cookie, THEN transaction id,
// before any attribute is interpreted. A datagram whose id does not match is
// reported as ErrNoMatch and its contents are never used, because trusting it
// would let anyone who can send us a UDP packet choose what we believe our own
// public address to be.
func Parse(msg []byte, txid TransactionID) (netip.AddrPort, bool, error) {
	if len(msg) < headerBytes || len(msg) > maxMessageBytes {
		return netip.AddrPort{}, false, ErrMalformed
	}
	kind := binary.BigEndian.Uint16(msg[0:2])
	length := int(binary.BigEndian.Uint16(msg[2:4]))
	// The two most significant bits must be zero (RFC 5389 §6); a nonzero pair
	// means this is not STUN at all (it is how STUN is multiplexed with other
	// protocols on one port).
	if kind&0xC000 != 0 {
		return netip.AddrPort{}, false, ErrMalformed
	}
	if binary.BigEndian.Uint32(msg[4:8]) != magicCookie {
		return netip.AddrPort{}, false, ErrMalformed
	}
	// Attribute lengths are 4-byte aligned, and the declared length must match
	// what actually arrived. A shorter body is truncation; a longer one is a
	// lying header, and both end the parse here rather than during a walk.
	if length%4 != 0 || headerBytes+length != len(msg) {
		return netip.AddrPort{}, false, ErrMalformed
	}
	var got TransactionID
	copy(got[:], msg[8:20])
	if got != txid {
		return netip.AddrPort{}, false, ErrNoMatch
	}
	if kind == bindingErrorResponse {
		return netip.AddrPort{}, false, ErrServerError
	}
	if kind != bindingSuccess {
		return netip.AddrPort{}, false, ErrMalformed
	}

	var mapped netip.AddrPort
	var haveMapped bool
	body := msg[headerBytes:]
	for len(body) > 0 {
		if len(body) < 4 {
			return netip.AddrPort{}, false, ErrMalformed
		}
		attrType := binary.BigEndian.Uint16(body[0:2])
		attrLen := int(binary.BigEndian.Uint16(body[2:4]))
		padded := (attrLen + 3) & ^3
		if attrLen > len(body)-4 || padded > len(body)-4 {
			return netip.AddrPort{}, false, ErrMalformed
		}
		value := body[4 : 4+attrLen]
		body = body[4+padded:]
		switch attrType {
		case attrXORMappedAddress:
			addr, err := decodeAddress(value, txid, true)
			if err != nil {
				return netip.AddrPort{}, false, err
			}
			// XOR form wins outright and returns immediately: it is the form a
			// NAT ALG cannot silently rewrite.
			return addr, false, nil
		case attrMappedAddress:
			addr, err := decodeAddress(value, txid, false)
			if err != nil {
				return netip.AddrPort{}, false, err
			}
			// Remembered, not returned: a later XOR-MAPPED-ADDRESS in the same
			// message is the better answer, so the legacy value is only a
			// fallback for pre-RFC-5389 servers.
			mapped, haveMapped = addr, true
		case attrAlternateServer:
			// Ignored on purpose. Following a redirect chosen by whoever answered
			// would turn one reply into an instruction to send packets anywhere.
		case attrErrorCode:
			// Only meaningful on an error response, which returned above.
		default:
			// Unknown comprehension-optional attributes are skipped, per
			// RFC 5389 §7.3.1. We require nothing but an address.
		}
	}
	if haveMapped {
		return mapped, true, nil
	}
	return netip.AddrPort{}, false, ErrNoAddress
}

// decodeAddress reads a (XOR-)MAPPED-ADDRESS value: reserved byte, family,
// port, address. The XOR key is the cookie for the port and IPv4 address, and
// cookie||transaction-id for IPv6 (RFC 5389 §15.2).
func decodeAddress(value []byte, txid TransactionID, xor bool) (netip.AddrPort, error) {
	if len(value) < 4 {
		return netip.AddrPort{}, ErrMalformed
	}
	family := value[1]
	port := binary.BigEndian.Uint16(value[2:4])
	raw := value[4:]
	var key [16]byte
	binary.BigEndian.PutUint32(key[0:4], magicCookie)
	copy(key[4:], txid[:])
	if xor {
		port ^= uint16(magicCookie >> 16)
	}
	var addr netip.Addr
	switch family {
	case familyIPv4:
		if len(raw) != 4 {
			return netip.AddrPort{}, ErrMalformed
		}
		var ip [4]byte
		copy(ip[:], raw)
		if xor {
			for i := range ip {
				ip[i] ^= key[i]
			}
		}
		addr = netip.AddrFrom4(ip)
	case familyIPv6:
		if len(raw) != 16 {
			return netip.AddrPort{}, ErrMalformed
		}
		var ip [16]byte
		copy(ip[:], raw)
		if xor {
			for i := range ip {
				ip[i] ^= key[i]
			}
		}
		addr = netip.AddrFrom16(ip)
	default:
		return netip.AddrPort{}, ErrMalformed
	}
	if !addr.IsValid() || port == 0 {
		return netip.AddrPort{}, ErrMalformed
	}
	return netip.AddrPortFrom(addr, port), nil
}

// EncodeBindingSuccess builds a success response for a request. It exists for
// the offline test responder in this package: there is no STUN server in the
// connector, and the only way to test the client without the network is to
// answer it locally.
func EncodeBindingSuccess(txid TransactionID, addr netip.AddrPort, xor bool) []byte {
	var value []byte
	port := addr.Port()
	ip := addr.Addr()
	var key [16]byte
	binary.BigEndian.PutUint32(key[0:4], magicCookie)
	copy(key[4:], txid[:])
	if xor {
		port ^= uint16(magicCookie >> 16)
	}
	header := []byte{0, familyIPv4}
	raw := ip.AsSlice()
	if !ip.Is4() {
		header[1] = familyIPv6
	}
	if xor {
		out := make([]byte, len(raw))
		for i := range raw {
			out[i] = raw[i] ^ key[i]
		}
		raw = out
	}
	value = append(value, header...)
	value = binary.BigEndian.AppendUint16(value, port)
	value = append(value, raw...)

	attrType := attrMappedAddress
	if xor {
		attrType = attrXORMappedAddress
	}
	attr := binary.BigEndian.AppendUint16(nil, attrType)
	attr = binary.BigEndian.AppendUint16(attr, uint16(len(value)))
	attr = append(attr, value...)
	for len(attr)%4 != 0 {
		attr = append(attr, 0)
	}
	msg := binary.BigEndian.AppendUint16(nil, bindingSuccess)
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(attr)))
	msg = binary.BigEndian.AppendUint32(msg, magicCookie)
	msg = append(msg, txid[:]...)
	return append(msg, attr...)
}

// RequestTransactionID extracts the transaction id from a well-formed request,
// for the offline responder. It returns false for anything that is not a STUN
// request, so the fake server is as strict as the client.
func RequestTransactionID(msg []byte) (TransactionID, bool) {
	var txid TransactionID
	if len(msg) < headerBytes || binary.BigEndian.Uint32(msg[4:8]) != magicCookie ||
		binary.BigEndian.Uint16(msg[0:2]) != bindingRequest {
		return txid, false
	}
	copy(txid[:], msg[8:20])
	return txid, true
}
