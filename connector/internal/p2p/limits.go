package p2p

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

// THE BYTE CEILING IS THE COST CONTROL — HARDENING-PLAN lines 877-879.
//
// Quoted: Circuit Relay v2 "can cap resources by connection count, time and
// bytes. That byte ceiling is the cost control: a fallback path cannot silently
// run up spend." TRANSPORT-NAT-DESIGN.md §3 rejected Cloudflare TURN because the
// bill lands on the company; §16 rejects company-run relays for the same reason.
// A relay path with no ceiling reintroduces exactly the exposure both rejections
// exist to prevent — and it does it on a DONOR's residential link, where §16 also
// warns about 1.2 TB ISP caps. So an unbounded relay path is not an incomplete
// implementation of this requirement, it is a failed one.
//
// THREE AXES, THREE DIFFERENT ENFORCEMENT POINTS, because they are knowable at
// three different moments and a single mechanism cannot cover all three:
//
//	axis        as relay SERVICE (we carry others' bytes)   as relay CLIENT (our bytes)
//	----------  -------------------------------------------  ---------------------------------
//	connections relayv2 Resources.MaxCircuits /              gater.InterceptAddrDial +
//	            MaxReservations, enforced inside libp2p       InterceptAccept, via Ledger
//	time        relayv2 RelayLimit.Duration resets the        Ledger circuit deadline, enforced
//	            circuit                                       by the Host watchdog
//	bytes       relayv2 RelayLimit.Data resets the circuit    Ledger, enforced by MeteredStream
//	                                                          on every Read/Write
//
// The service-side column is libp2p's own accounting and we only configure it.
// The client-side column is ours, because libp2p imposes no ceiling on bytes we
// push through someone else's relay — the relay operator's limits do, and relying
// on a counterparty to enforce our budget would mean we cannot state a bound.
//
// WHY BYTES ARE METERED AT THE STREAM AND NOT AT THE CONNECTION. libp2p does not
// expose a byte counter on network.Conn, and wrapping the relay transport to count
// at the socket would mean forking a transport. Every byte of customer data this
// package moves goes through a stream we opened or accepted on our own protocol
// ID, so metering there is complete for the traffic that matters, and it is
// honestly incomplete for libp2p's own protocol chatter (identify, ping, DCUtR
// signalling) which is kilobytes, not gigabytes. Limits.Stated() says so rather
// than implying the number is every byte on the wire.

// Limits is the owner-configurable ceiling set. Zero means "use the reviewed
// default", never "unlimited": a config field an owner left blank must not be the
// field that uncaps spend. Unlimited is not expressible on purpose.
type Limits struct {
	// MaxRelayedConns caps simultaneous relayed circuits in either direction,
	// ours and theirs. Direct (hole-punched or LAN) connections are NOT counted:
	// they cost nobody anything, so capping them would throttle the good path to
	// protect against the bad one.
	MaxRelayedConns int `json:"maxRelayedConns,omitempty"`
	// MaxCircuitSeconds is the wall-clock life of one relayed circuit. A circuit
	// that has been open for hours is either a stalled transfer or a leak; both
	// want the same treatment.
	MaxCircuitSeconds uint64 `json:"maxCircuitSeconds,omitempty"`
	// MaxCircuitBytes is the per-circuit byte ceiling.
	MaxCircuitBytes uint64 `json:"maxCircuitBytes,omitempty"`
	// MaxTotalBytes is the ceiling across ALL relayed circuits for the lifetime
	// of this process. It is the one that actually bounds spend: a per-circuit cap
	// alone is trivially defeated by opening more circuits.
	//
	// It is deliberately NOT a monthly budget. HARDENING-PLAN §16 requires a
	// per-donor MONTHLY bandwidth budget with cap-aware scheduling, and that needs
	// persistence across restarts plus a calendar, neither of which exists in the
	// connector today. Claiming a monthly cap while resetting it on every restart
	// would be worse than claiming a process-lifetime cap, which is what this is.
	// See Stated().
	MaxTotalBytes uint64 `json:"maxTotalBytes,omitempty"`
	// OfferRelayService makes THIS host a Circuit Relay v2 service for other
	// peers — the "publicly dialable donors, paid for it" role in §16. Default
	// false: carrying other people's bytes over an owner's residential link is
	// never something a binary upgrade should start doing.
	OfferRelayService bool `json:"offerRelayService,omitempty"`
	// MaxServiceReservations caps how many peers may hold a reservation on us
	// when OfferRelayService is set.
	MaxServiceReservations int `json:"maxServiceReservations,omitempty"`
}

// Reviewed defaults. They are small on purpose: a relay is the EXPENSIVE path and
// the fallback of last resort, so the default should be enough to move a modest
// object and not enough to move a 500 GB backup unnoticed. An owner who wants
// more must say so, which is the point at which they also see what it costs.
const (
	DefaultMaxRelayedConns        = 4
	DefaultMaxCircuitSeconds      = 15 * 60
	DefaultMaxCircuitBytes        = 256 << 20 // 256 MiB
	DefaultMaxTotalBytes          = 2 << 30   // 2 GiB per process lifetime
	DefaultMaxServiceReservations = 16

	// Hard outer bounds. An owner may raise the ceilings but not to a value that
	// is effectively unlimited, because "effectively unlimited" is the state §16
	// says must not be reachable. These are generous, and they exist so that a
	// fat-fingered config cannot become an uncapped relay.
	maxAllowedRelayedConns   = 64
	maxAllowedCircuitSeconds = 24 * 60 * 60
	maxAllowedCircuitBytes   = 64 << 30  // 64 GiB
	maxAllowedTotalBytes     = 512 << 30 // 512 GiB
)

var errLimits = errors.New("p2p: relay limits are out of range; ceilings may be raised but not removed")

// Normalize fills defaults and rejects out-of-range values. It returns a copy so
// a caller's struct is never mutated behind its back.
func (l Limits) Normalize() (Limits, error) {
	if l.MaxRelayedConns == 0 {
		l.MaxRelayedConns = DefaultMaxRelayedConns
	}
	if l.MaxCircuitSeconds == 0 {
		l.MaxCircuitSeconds = DefaultMaxCircuitSeconds
	}
	if l.MaxCircuitBytes == 0 {
		l.MaxCircuitBytes = DefaultMaxCircuitBytes
	}
	if l.MaxTotalBytes == 0 {
		l.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if l.MaxServiceReservations == 0 {
		l.MaxServiceReservations = DefaultMaxServiceReservations
	}
	if l.MaxRelayedConns < 1 || l.MaxRelayedConns > maxAllowedRelayedConns ||
		l.MaxCircuitSeconds > maxAllowedCircuitSeconds ||
		l.MaxCircuitBytes > maxAllowedCircuitBytes ||
		l.MaxTotalBytes > maxAllowedTotalBytes ||
		l.MaxServiceReservations < 1 || l.MaxServiceReservations > maxAllowedRelayedConns {
		return Limits{}, errLimits
	}
	// A per-circuit ceiling above the total is not an error, it is a
	// contradiction: the total would silently win and the owner's per-circuit
	// number would be a lie. Clamp it and keep one meaning per field.
	if l.MaxCircuitBytes > l.MaxTotalBytes {
		l.MaxCircuitBytes = l.MaxTotalBytes
	}
	return l, nil
}

// Stated is the sentence a surface prints. It says what the caps DO cover and
// what they do not, because the failure mode of a cost control is an owner who
// believed it covered more than it did.
func (l Limits) Stated() string {
	return "relay ceilings: at most " + itoa(int64(l.MaxRelayedConns)) + " concurrent relayed circuits, " +
		itoa(int64(l.MaxCircuitSeconds)) + "s and " + itoa(int64(l.MaxCircuitBytes)) + " bytes per circuit, " +
		itoa(int64(l.MaxTotalBytes)) + " bytes total for this process run. " +
		"The byte counters cover data streams on the nexal relay protocol, not libp2p's own control chatter, " +
		"and the total resets when the process restarts — this is NOT the per-donor monthly budget HARDENING-PLAN §16 requires, which needs persistence that does not exist yet"
}

// RelayResources translates the owner's limits into libp2p's relay-service
// accounting, for the case where this host carries other peers' bytes.
func (l Limits) RelayResources() relayv2.Resources {
	r := relayv2.DefaultResources()
	r.MaxReservations = l.MaxServiceReservations
	r.MaxCircuits = l.MaxRelayedConns
	r.Limit = &relayv2.RelayLimit{
		Duration: time.Duration(l.MaxCircuitSeconds) * time.Second,
		// relayv2 applies Data per direction. Halving keeps the owner's single
		// number meaning "bytes this circuit may move", instead of quietly
		// meaning twice that.
		Data: int64(l.MaxCircuitBytes / 2),
	}
	return r
}

// Consumption is the surfaced state. Every ceiling appears next to its usage so
// a surface can show "3.1 of 2048 MiB" rather than a bare number the owner has to
// look the limit up for.
type Consumption struct {
	Limits Limits `json:"limits"`

	RelayedCircuitsOpen int    `json:"relayedCircuitsOpen"`
	RelayedCircuitsEver uint64 `json:"relayedCircuitsEver"`
	RelayBytesUsed      uint64 `json:"relayBytesUsed"`
	RelayBytesRemaining uint64 `json:"relayBytesRemaining"`

	// CircuitsRefused counts circuits denied by the connection ceiling,
	// CircuitsTruncated counts circuits cut off by the byte or time ceiling.
	// They are separate because they mean different things to an owner: refused
	// is "we protected you", truncated is "a transfer failed and here is why".
	CircuitsRefused   uint64 `json:"circuitsRefused"`
	CircuitsTruncated uint64 `json:"circuitsTruncated"`

	Exhausted bool   `json:"exhausted"`
	Note      string `json:"note"`
}

// Ledger is the client-side accounting for all three ceilings. It is the only
// mutable state behind the cost control, so it is one type with one mutex rather
// than counters spread across the host.
type Ledger struct {
	limits Limits
	now    func() time.Time

	mu        sync.Mutex
	open      map[*circuit]struct{}
	everOpen  uint64
	refused   uint64
	truncated uint64
	used      uint64
}

// circuit is one relayed circuit's accounting: its own byte counter and deadline.
type circuit struct {
	ledger   *Ledger
	peer     peer.ID
	started  time.Time
	deadline time.Time
	used     atomic.Uint64
	closed   atomic.Bool
	conn     network.Conn // may be nil in tests that exercise accounting only
}

// NewLedger normalizes the limits and returns the accounting. It never fails
// open: an invalid Limits is an error, not a fallback to unlimited.
func NewLedger(l Limits, clock func() time.Time) (*Ledger, error) {
	n, err := l.Normalize()
	if err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	return &Ledger{limits: n, now: clock, open: make(map[*circuit]struct{})}, nil
}

func (l *Ledger) Limits() Limits { return l.limits }

// CircuitAvailable answers the connection-count ceiling and the total-byte
// ceiling together, because a host that has spent its byte budget must stop
// opening circuits as well as stop writing on them — otherwise it accumulates
// circuits it can never use and looks broken instead of capped.
func (l *Ledger) CircuitAvailable() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used >= l.limits.MaxTotalBytes {
		l.refused++
		return false
	}
	if len(l.open) >= l.limits.MaxRelayedConns {
		l.refused++
		return false
	}
	return true
}

// Open books a relayed circuit. The returned release must be called exactly once.
func (l *Ledger) Open(p peer.ID, conn network.Conn) (*circuit, error) {
	l.mu.Lock()
	if l.used >= l.limits.MaxTotalBytes || len(l.open) >= l.limits.MaxRelayedConns {
		l.refused++
		l.mu.Unlock()
		return nil, ErrBudget
	}
	start := l.now()
	c := &circuit{
		ledger:   l,
		peer:     p,
		started:  start,
		deadline: start.Add(time.Duration(l.limits.MaxCircuitSeconds) * time.Second),
		conn:     conn,
	}
	l.open[c] = struct{}{}
	l.everOpen++
	l.mu.Unlock()
	return c, nil
}

// charge adds n bytes and reports whether the circuit may continue. It checks
// per-circuit and total in the same critical section so two concurrent streams
// cannot both observe headroom that only one of them can have.
func (l *Ledger) charge(c *circuit, n uint64) error {
	if n == 0 {
		return nil
	}
	l.mu.Lock()
	l.used += n
	total := l.used
	l.mu.Unlock()
	per := c.used.Add(n)
	if per >= l.limits.MaxCircuitBytes || total >= l.limits.MaxTotalBytes {
		l.cut(c)
		return ErrBudget
	}
	return nil
}

// check is the pre-flight form of charge: may this circuit spend at all. It is
// separate because charge(0) must be a no-op (a zero-length read is not a
// ceiling event) while a pre-write check must still refuse an exhausted circuit.
func (l *Ledger) check(c *circuit) error {
	if c.closed.Load() {
		return ErrBudget
	}
	l.mu.Lock()
	total := l.used
	l.mu.Unlock()
	if total >= l.limits.MaxTotalBytes || c.used.Load() >= l.limits.MaxCircuitBytes {
		l.cut(c)
		return ErrBudget
	}
	if c.Expired(l.now()) {
		l.cut(c)
		return ErrBudget
	}
	return nil
}

// cut closes a circuit that hit a ceiling. Closing the underlying connection is
// the enforcement: returning an error to our own caller would leave the circuit
// open for the next caller to keep spending on.
func (l *Ledger) cut(c *circuit) {
	if c.closed.Swap(true) {
		return
	}
	l.mu.Lock()
	delete(l.open, c)
	l.truncated++
	l.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// Close releases a circuit that finished normally.
func (c *circuit) Close() {
	if c.closed.Swap(true) {
		return
	}
	c.ledger.mu.Lock()
	delete(c.ledger.open, c)
	c.ledger.mu.Unlock()
}

// Used reports this circuit's byte count, for tests and surfaces.
func (c *circuit) Used() uint64 { return c.used.Load() }

// Expired reports whether the circuit has outlived the time ceiling.
func (c *circuit) Expired(now time.Time) bool { return !now.Before(c.deadline) }

// EnforceTime cuts every circuit past its deadline and returns how many it cut.
// It is called on a ticker by the Host rather than being checked on I/O, because
// the reason to cap duration is a circuit that has STOPPED doing I/O — a check
// that only runs on a Read would never fire for exactly the case it is for.
func (l *Ledger) EnforceTime() int {
	now := l.now()
	l.mu.Lock()
	var expired []*circuit
	for c := range l.open {
		if c.Expired(now) {
			expired = append(expired, c)
		}
	}
	l.mu.Unlock()
	for _, c := range expired {
		l.cut(c)
	}
	return len(expired)
}

// Consumption snapshots everything a surface needs.
func (l *Ledger) Consumption() Consumption {
	l.mu.Lock()
	defer l.mu.Unlock()
	remaining := uint64(0)
	if l.limits.MaxTotalBytes > l.used {
		remaining = l.limits.MaxTotalBytes - l.used
	}
	return Consumption{
		Limits:              l.limits,
		RelayedCircuitsOpen: len(l.open),
		RelayedCircuitsEver: l.everOpen,
		RelayBytesUsed:      l.used,
		RelayBytesRemaining: remaining,
		CircuitsRefused:     l.refused,
		CircuitsTruncated:   l.truncated,
		Exhausted:           remaining == 0,
		Note:                l.limits.Stated(),
	}
}

// MeteredStream is the byte ceiling's enforcement point. Every relayed data
// stream is wrapped, so the cap applies to reads as well as writes: a restore
// pulls bytes IN over the relay and costs the relay operator exactly as much as a
// backup pushing them out.
type MeteredStream struct {
	network.Stream
	circuit *circuit
}

// Meter wraps a stream against a circuit's budget. A direct (non-relayed) stream
// must not be wrapped — see Host.wrap.
func Meter(s network.Stream, c *circuit) *MeteredStream {
	return &MeteredStream{Stream: s, circuit: c}
}

func (m *MeteredStream) Read(p []byte) (int, error) {
	n, err := m.Stream.Read(p)
	if n > 0 {
		if cerr := m.circuit.ledger.charge(m.circuit, uint64(n)); cerr != nil {
			// The bytes already read are returned — discarding them would corrupt
			// the caller's stream position for no benefit — but the stream is
			// reset so no further byte can be spent.
			_ = m.Stream.Reset()
			return n, cerr
		}
	}
	return n, err
}

func (m *MeteredStream) Write(p []byte) (int, error) {
	// Refuse BEFORE writing when the budget is already gone: a write that has
	// left the machine has already cost the relay operator, so the check has to
	// precede the syscall, not follow it.
	if err := m.circuit.ledger.check(m.circuit); err != nil {
		return 0, err
	}
	n, err := m.Stream.Write(p)
	if n > 0 {
		if cerr := m.circuit.ledger.charge(m.circuit, uint64(n)); cerr != nil {
			_ = m.Stream.Reset()
			return n, cerr
		}
	}
	return n, err
}

// Used is the running total for this stream's circuit.
func (m *MeteredStream) Used() uint64 { return m.circuit.Used() }

var _ io.ReadWriter = (*MeteredStream)(nil)

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
