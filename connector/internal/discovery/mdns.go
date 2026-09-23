package discovery

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// LAN discovery: DNS-SD over multicast DNS, hand-rolled on the standard library.
//
// What this file produces is a list of *candidates*: machines that claimed, over
// unauthenticated multicast, to be neXal hosts. A candidate is a hint about where
// to look. It is never evidence of membership, and nothing here may widen the
// mutual-TLS fingerprint allowlist in internal/pool. See peers.go.
const (
	// ServiceName is the DNS-SD service type, fully qualified.
	ServiceName = "_nexal._tcp.local."
	// MulticastPort is the mDNS port for both address families.
	MulticastPort = 5353
	// defaultTTL is deliberately short. A stale SRV pointing at an address that
	// has moved is worse than no record: it sends a dial at whoever holds the
	// address now.
	defaultTTL = 120
	// maxCandidatesPerPacket bounds the work one datagram can create.
	maxCandidatesPerPacket = 16
	// maxSockets bounds how many interfaces we join.
	maxSockets = 8
	// readBuffer is one byte over the decode bound so an oversized datagram is
	// detected rather than silently truncated into something parseable.
	readBuffer = maxMessageBytes + 1
)

// MulticastIPv4 and MulticastIPv6 are the two mDNS groups.
var (
	MulticastIPv4 = netip.AddrPortFrom(netip.MustParseAddr("224.0.0.251"), MulticastPort)
	MulticastIPv6 = netip.AddrPortFrom(netip.MustParseAddr("ff02::fb"), MulticastPort)
)

var (
	// ErrIgnored means the packet was not ours or did not deserve a reply. It is
	// the normal outcome on a busy network and is not worth logging per packet.
	ErrIgnored = errors.New("discovery: packet ignored")
	// ErrRateLimited means we would have replied but refused to, because a reply
	// rate above this is how a discovery responder becomes an amplifier.
	ErrRateLimited = errors.New("discovery: multicast response rate limited")
	// ErrSharingDisabled is returned when LAN advertising or browsing is
	// attempted while the gate is closed. The gate is closed by default; see
	// SharingPolicy.
	ErrSharingDisabled = errors.New("discovery: LAN discovery is disabled by policy")
	// ErrInvalid marks a caller-supplied configuration error.
	ErrInvalid = errors.New("discovery: invalid configuration")
)

// Advertisement is what this host is willing to say about itself on the local
// link. It contains no secret: the fingerprint is a public key hash that the
// peer TLS already pins, which is exactly why publishing it is safe and why
// rendezvous publishes the same value instead of inventing a second identity.
type Advertisement struct {
	HostID      string // Instance label; also the SRV target's first label.
	Fingerprint string // pool.DeviceID: 64 lowercase hex characters.
	Version     string // Agent version, for display and for skew diagnosis.
	Port        uint16 // The peer object-transfer port.
	Addresses   []netip.Addr
}

func (a Advertisement) validate() error {
	if !ValidHostID(a.HostID) || !ValidFingerprint(a.Fingerprint) ||
		!validTXTValue(a.Version) || a.Port == 0 {
		return ErrInvalid
	}
	if len(a.Addresses) > 8 {
		return ErrInvalid
	}
	for _, addr := range a.Addresses {
		// Only private, loopback, CGNAT or link-local addresses are advertised.
		// A public address on the local link is either a misconfiguration or a
		// mistake we should not help somebody make.
		if !addr.IsValid() || !localAddress(addr) {
			return ErrInvalid
		}
	}
	return nil
}

func (a Advertisement) instance() string { return a.HostID + "." + ServiceName }
func (a Advertisement) hostname() string { return a.HostID + ".local." }

// txt carries exactly the three keys the contract specifies. Capabilities are
// not advertised over multicast: they are self-reported everywhere, but at least
// the coordinator's copy arrives over an authenticated channel.
func (a Advertisement) txt() []string {
	return []string{"fp=" + a.Fingerprint, "v=" + a.Version, "h=" + a.HostID}
}

// records builds the PTR/SRV/TXT set plus address records.
func (a Advertisement) records() ([]record, []record, error) {
	if err := a.validate(); err != nil {
		return nil, nil, err
	}
	answers := []record{
		{name: ServiceName, rtype: typePTR, class: classIN, ttl: defaultTTL, ptr: a.instance()},
		{name: a.instance(), rtype: typeSRV, class: classIN, ttl: defaultTTL, port: a.Port, target: a.hostname()},
		{name: a.instance(), rtype: typeTXT, class: classIN, ttl: defaultTTL, txt: a.txt()},
	}
	var extra []record
	for _, addr := range a.Addresses {
		rr := record{name: a.hostname(), class: classIN, ttl: defaultTTL, addr: addr.Unmap()}
		if rr.addr.Is4() {
			rr.rtype = typeA
		} else {
			rr.rtype = typeAAAA
		}
		extra = append(extra, rr)
	}
	return answers, extra, nil
}

// Candidate is one unauthenticated claim heard on the local link.
type Candidate struct {
	Instance    string
	HostID      string
	Fingerprint string
	Version     string
	Port        uint16
	Addresses   []netip.Addr
	// From is the source address of the datagram. It is the only field here that
	// the sender could not simply make up, and even it is only as trustworthy as
	// the local link.
	From   netip.Addr
	SeenAt time.Time
}

// QueryPacket builds the browse query: one PTR question for the service type.
// It asks for nothing else, so a response we provoke is bounded in size.
func QueryPacket() ([]byte, error) {
	return encodeMessage(&message{questions: []question{
		{name: ServiceName, qtype: typePTR, class: classIN},
	}})
}

// ParseCandidates decodes a datagram and extracts neXal candidates from it.
//
// Everything about this function is suspicious of its input by construction: it
// requires a well-formed fingerprint and host id, refuses a TXT record whose
// claimed host id disagrees with the instance label, and caps how many
// candidates a single packet can produce.
func ParseCandidates(payload []byte, from netip.AddrPort, now time.Time) ([]Candidate, error) {
	m, err := decodeMessage(payload)
	if err != nil {
		return nil, err
	}
	if !m.response() {
		return nil, ErrIgnored
	}
	all := append(append([]record{}, m.answers...), m.extra...)
	instances := map[string]bool{}
	for _, rr := range all {
		if rr.rtype == typePTR && rr.class == classIN && rr.name == ServiceName &&
			strings.HasSuffix(rr.ptr, "."+ServiceName) && len(instances) < maxCandidatesPerPacket {
			instances[rr.ptr] = true
		}
	}
	// A responder may send SRV/TXT without a PTR (a direct instance query
	// answer). Accept those instances too, under the same cap.
	for _, rr := range all {
		if (rr.rtype == typeSRV || rr.rtype == typeTXT) && strings.HasSuffix(rr.name, "."+ServiceName) &&
			len(instances) < maxCandidatesPerPacket {
			instances[rr.name] = true
		}
	}
	addresses := map[string][]netip.Addr{}
	for _, rr := range all {
		if rr.rtype == typeA || rr.rtype == typeAAAA {
			if addr := rr.addr.Unmap(); addr.IsValid() && localAddress(addr) && len(addresses[rr.name]) < 8 {
				addresses[rr.name] = append(addresses[rr.name], addr)
			}
		}
	}
	var out []Candidate
	for instance := range instances {
		c := Candidate{Instance: instance, From: from.Addr().Unmap(), SeenAt: now}
		var target string
		for _, rr := range all {
			if rr.name != instance {
				continue
			}
			switch rr.rtype {
			case typeSRV:
				c.Port, target = rr.port, rr.target
			case typeTXT:
				applyTXT(&c, rr.txt)
			}
		}
		if c.Port == 0 || !ValidFingerprint(c.Fingerprint) || !ValidHostID(c.HostID) {
			continue // Incomplete or unusable claim: drop it, do not guess.
		}
		// The instance label is the host id. A packet that disagrees with itself
		// is not worth reconciling.
		if c.HostID+"."+ServiceName != instance {
			continue
		}
		c.Addresses = addresses[target]
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, ErrIgnored
	}
	return out, nil
}

func applyTXT(c *Candidate, txt []string) {
	for _, entry := range txt {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !validTXTValue(value) {
			continue
		}
		switch key {
		case "fp":
			c.Fingerprint = value
		case "v":
			c.Version = value
		case "h":
			c.HostID = value
		}
	}
}

// Responder answers browse queries for this host's own records and nothing else.
//
// Two rules keep a responder from becoming an amplifier: a reply is built solely
// from our own fixed advertisement (no request bytes are echoed), and replies are
// rate limited globally and per source. When the limiter is exhausted the
// responder is silent, because silence is the safe failure for a discovery
// mechanism that grants nothing.
type Responder struct {
	answers []record
	extra   []record
	now     func() time.Time

	mu     sync.Mutex
	global bucket
	// perSource is bounded; once full, unknown sources get silence rather than
	// an eviction policy an attacker could cycle.
	perSource map[netip.Addr]*bucket
}

const maxTrackedSources = 256

func NewResponder(ad Advertisement, clock func() time.Time) (*Responder, error) {
	answers, extra, err := ad.records()
	if err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	return &Responder{
		answers: answers, extra: extra, now: clock,
		global:    bucket{capacity: 10, refill: time.Second / 5, tokens: 10, updated: clock()},
		perSource: map[netip.Addr]*bucket{},
	}, nil
}

// Respond returns the datagram to send, or an error meaning "stay silent".
func (r *Responder) Respond(payload []byte, from netip.AddrPort) ([]byte, error) {
	m, err := decodeMessage(payload)
	if err != nil {
		return nil, err
	}
	// Never answer a response, a non-query opcode, or a truncated query we would
	// have to reassemble. Never answer a sender that is not on a local network.
	if m.response() || m.truncated() || m.flags&flagOpcode != 0 || m.flags&flagRcode != 0 {
		return nil, ErrIgnored
	}
	if !localAddress(from.Addr().Unmap()) {
		return nil, ErrIgnored
	}
	wanted := false
	for _, q := range m.questions {
		if q.class != classIN && q.class != typeANY {
			continue
		}
		switch {
		case q.name == ServiceName && (q.qtype == typePTR || q.qtype == typeANY):
			wanted = true
		case q.name == r.instanceName() && (q.qtype == typeSRV || q.qtype == typeTXT || q.qtype == typeANY):
			wanted = true
		}
	}
	if !wanted {
		return nil, ErrIgnored
	}
	if err := r.allow(from.Addr().Unmap()); err != nil {
		return nil, err
	}
	// The reply carries our own records only. Its size is a function of this
	// host's configuration, never of the request, so the response/request ratio
	// cannot be inflated by asking many questions in one packet.
	return encodeMessage(&message{
		flags:   flagResponse,
		answers: r.answers,
		extra:   r.extra,
	})
}

func (r *Responder) instanceName() string {
	if len(r.answers) == 0 {
		return ""
	}
	return r.answers[0].ptr
}

func (r *Responder) allow(from netip.Addr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !r.global.take(now) {
		return ErrRateLimited
	}
	b := r.perSource[from]
	if b == nil {
		if len(r.perSource) >= maxTrackedSources {
			return ErrRateLimited
		}
		b = &bucket{capacity: 3, refill: time.Second, tokens: 3, updated: now}
		r.perSource[from] = b
	}
	if !b.take(now) {
		return ErrRateLimited
	}
	return nil
}

// bucket is a token bucket with an injected clock. It holds no timer, so a
// Responder needs no goroutine and no Close.
type bucket struct {
	capacity int
	refill   time.Duration // Time to earn one token.
	tokens   int
	updated  time.Time
}

func (b *bucket) take(now time.Time) bool {
	if elapsed := now.Sub(b.updated); elapsed > 0 && b.refill > 0 {
		if earned := int(elapsed / b.refill); earned > 0 {
			b.tokens = min(b.capacity, b.tokens+earned)
			b.updated = now
		}
	} else if now.Before(b.updated) {
		// A clock that moved backwards must not mint tokens.
		b.updated = now
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// MDNS is the socket-level glue: it joins the mDNS groups on multicast-capable
// interfaces, answers queries through Responder and reports candidates.
//
// It is constructed only when the LAN gate in SharingPolicy is open. A full
// tunnel VPN can capture the default route and block local-subnet traffic, so a
// zero-candidate result is an expected state to explain in the UI, not an error
// to retry harder (HARDENING-PLAN §34.3).
type MDNS struct {
	responder *Responder
	query     []byte
	logger    *slog.Logger
	now       func() time.Time

	mu     sync.Mutex
	conns  []*net.UDPConn
	closed bool
}

// Options configures the LAN listener.
type Options struct {
	Advertisement Advertisement
	Sharing       SharingPolicy
	Clock         func() time.Time
	Logger        *slog.Logger
}

// Listen joins the mDNS groups. It fails closed: if the policy gate is shut, no
// socket is opened and nothing is announced.
func Listen(opts Options) (*MDNS, error) {
	if !opts.Sharing.LANDiscovery {
		return nil, ErrSharingDisabled
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	responder, err := NewResponder(opts.Advertisement, clock)
	if err != nil {
		return nil, err
	}
	query, err := QueryPacket()
	if err != nil {
		return nil, err
	}
	m := &MDNS{responder: responder, query: query, logger: logger, now: clock}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range interfaces {
		if len(m.conns) >= maxSockets {
			break
		}
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 ||
			iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		for _, group := range []netip.AddrPort{MulticastIPv4, MulticastIPv6} {
			conn, err := net.ListenMulticastUDP(udpNetwork(group.Addr()),
				&iface, &net.UDPAddr{IP: group.Addr().AsSlice(), Port: int(group.Port())})
			if err != nil {
				// One address family or one interface refusing the join is
				// ordinary on a Mac with a VPN or a disabled family. Record it
				// and carry on; a total failure is reported below.
				m.logger.Debug("mdns join refused", "interface", iface.Name, "family", udpNetwork(group.Addr()))
				continue
			}
			_ = conn.SetReadBuffer(64 << 10)
			m.conns = append(m.conns, conn)
		}
	}
	if len(m.conns) == 0 {
		return nil, errors.New("discovery: no multicast-capable interface accepted an mDNS join")
	}
	return m, nil
}

func udpNetwork(addr netip.Addr) string {
	if addr.Is4() {
		return "udp4"
	}
	return "udp6"
}

// Run reads until ctx is done, delivering candidates to sink. sink must not
// block; it is called from the read goroutines.
func (m *MDNS) Run(ctx context.Context, sink func([]Candidate)) {
	var wg sync.WaitGroup
	for _, conn := range m.conns {
		wg.Add(1)
		go func(conn *net.UDPConn) {
			defer wg.Done()
			buf := make([]byte, readBuffer)
			for ctx.Err() == nil {
				_ = conn.SetReadDeadline(m.now().Add(time.Second))
				n, from, err := conn.ReadFromUDPAddrPort(buf)
				if err != nil {
					var timeout net.Error
					if errors.As(err, &timeout) && timeout.Timeout() {
						continue
					}
					return
				}
				m.handle(buf[:n], from, conn, sink)
			}
		}(conn)
	}
	<-ctx.Done()
	m.Close()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		m.logger.Debug("mdns read shutdown exceeded deadline")
	}
}

func (m *MDNS) handle(payload []byte, from netip.AddrPort, conn *net.UDPConn, sink func([]Candidate)) {
	// A single datagram is both a possible query and a possible response; parse
	// it as each, independently, and let both paths reject it.
	if reply, err := m.responder.Respond(payload, from); err == nil {
		group := MulticastIPv4
		if !from.Addr().Unmap().Is4() {
			group = MulticastIPv6
		}
		if _, err := conn.WriteToUDPAddrPort(reply, group); err != nil {
			m.logger.Debug("mdns reply failed")
		}
	}
	if candidates, err := ParseCandidates(payload, from, m.now()); err == nil && sink != nil {
		sink(candidates)
	}
}

// Browse sends one PTR query. Responses arrive on the same sockets Run reads.
func (m *MDNS) Browse() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return net.ErrClosed
	}
	var sent int
	for _, conn := range m.conns {
		group := MulticastIPv4
		if conn.LocalAddr() != nil && strings.Contains(conn.LocalAddr().Network(), "6") {
			group = MulticastIPv6
		}
		if _, err := conn.WriteToUDPAddrPort(m.query, group); err == nil {
			sent++
		}
	}
	if sent == 0 {
		return errors.New("discovery: no multicast query could be sent")
	}
	return nil
}

func (m *MDNS) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	conns := append([]*net.UDPConn(nil), m.conns...)
	m.mu.Unlock()
	for _, conn := range conns {
		// A deadline explicitly wakes multicast reads on macOS versions where a
		// concurrent Close alone may not interrupt ReadFrom promptly.
		_ = conn.SetReadDeadline(time.Now())
		go func(c *net.UDPConn) { _ = c.Close() }(conn)
	}
}
