package p2p

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/event"
	libhost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	relayclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	ma "github.com/multiformats/go-multiaddr"

	"nexal/connector/internal/pool"
	"nexal/connector/internal/stun"
)

// Protocol is the single application protocol this package speaks. Everything
// else on the host is libp2p's own machinery (identify, ping, DCUtR, relay).
//
// It is versioned in the string because a protocol ID is the only negotiation
// point available: two connectors on different releases must fail to negotiate
// rather than half-speak an older framing.
const Protocol protocol.ID = "/nexal/transfer/1.0.0"

// DefaultListen binds loopback only.
//
// This is not a placeholder, it is the safe default for a feature whose flag is
// off: a host that is never enabled must never bind a routable interface, and an
// owner who enables it must say which address to bind. It also happens to be what
// the tests use, which is deliberate — the tested configuration is the default
// configuration.
var DefaultListen = []string{"/ip4/127.0.0.1/tcp/0"}

// Options configures the data plane. The zero value is DISABLED, which is the
// whole feature-flag design: a caller that forgets to set anything gets a Host
// that does nothing rather than a Host with defaults it did not choose.
type Options struct {
	// Enabled is the feature flag. False means New returns a Host whose every
	// method returns ErrDisabled, which starts no listener, binds no port,
	// publishes no address and holds no reservation. HARDENING-PLAN's shipping
	// rule: a new transport must not activate on upgrade.
	Enabled bool
	// Identity is this host's pool identity. The libp2p peer ID derives from it,
	// so peer ID and pool.DeviceID describe one keypair (see identity.go).
	Identity pool.Identity
	// Authorizer is the coordinator's authorized set. REQUIRED when Enabled:
	// without it nothing can be authorized, and a host that cannot authorize
	// anybody is a host that must not start rather than one that allows
	// everybody. New refuses it as a wiring error.
	Authorizer Authorizer
	// Limits are the relay ceilings. Zero fields take the reviewed defaults.
	Limits Limits
	// Listen are multiaddrs to bind. Empty means DefaultListen (loopback).
	Listen []string
	// Rendezvous is the coordinator signalling seam. Nil is legal and means this
	// host can accept and can serve, but cannot initiate to a peer whose address
	// it does not already hold. See signalling.go for the endpoints required.
	Rendezvous Rendezvous
	// STUN is a Phase 1 observation, optional, used as the cheap punchability
	// pre-check. Nil is normal; the probe is opt-in. See nat.go.
	STUN *stun.Result
	// ForceReachability overrides AutoNAT's verdict. Two real reasons, and it is
	// not a test hook bolted on:
	//
	//  1. AutoNAT structurally cannot return a verdict on a host with no public
	//     addresses — it asks peers to dial addresses it has advertised, and
	//     loopback addresses are filtered out before it ever tries. So an OFFLINE
	//     test of the relay-SERVICE path (which libp2p only starts once it believes
	//     it is publicly reachable) has no other way to exist, and the alternative
	//     is shipping that path untested.
	//  2. An owner with a manual port forward knows something AutoNAT will get
	//     wrong if their peers are all behind the same NAT and cannot dial back in.
	//
	// It is a pointer so "unset" is distinguishable from "forced unknown", and it
	// changes only DIALABILITY. It grants no authorization and cannot: the gater
	// does not consult it.
	ForceReachability *network.Reachability
	// Clock is injectable so the time ceiling can be tested without sleeping.
	Clock func() time.Time
}

// Host is the libp2p data plane. It is safe for concurrent use and it is valid
// (and inert) when disabled.
type Host struct {
	opts    Options
	enabled bool
	limits  Limits

	host   libhost.Host
	gater  *gater
	ledger *Ledger
	relay  *relayv2.Relay
	punch  *punchTracer

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu           sync.Mutex
	reach        network.Reachability
	reachSeen    bool
	reservations map[peer.ID]relayReservation
	closed       bool
}

type relayReservation struct {
	Expiration    time.Time
	LimitDuration time.Duration
	LimitData     uint64
	// addrs are the RELAY's own transport addresses. They are kept because a
	// circuit multiaddr a peer can actually dial is <relay transport
	// addr>/p2p/<relay id>/p2p-circuit — publishing only /p2p/<relay>/p2p-circuit
	// would require the peer to already know how to reach the relay, which is the
	// problem the address was supposed to solve.
	addrs []ma.Multiaddr
}

// New builds the data plane. When Enabled is false it returns immediately with an
// inert Host and touches nothing — no key is loaded, no socket is opened, and
// libp2p's constructor is never called, so an owner who has not enabled this
// pays not even the goroutines.
func New(o Options) (*Host, error) {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if !o.Enabled {
		return &Host{opts: o, enabled: false, reach: network.ReachabilityUnknown}, nil
	}
	if o.Authorizer == nil {
		return nil, ErrInvalid
	}
	limits, err := o.Limits.Normalize()
	if err != nil {
		return nil, err
	}
	priv, err := PrivateKey(o.Identity)
	if err != nil {
		return nil, err
	}
	ledger, err := NewLedger(limits, o.Clock)
	if err != nil {
		return nil, err
	}
	g := newGater(o.Authorizer, ledger)
	tracer := &punchTracer{}

	listen := o.Listen
	if len(listen) == 0 {
		listen = DefaultListen
	}

	libopts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(listen...),
		// The gater is the §30.2 boundary. It is passed here rather than checked
		// by callers so there is no code path into this host that skips it.
		libp2p.ConnectionGater(g),
		// DCUtR. The tracer is how RTT and punch outcomes reach Snapshot; without
		// it the punch would be invisible and "did it work" would be unanswerable.
		libp2p.EnableHolePunching(holepunch.WithTracer(tracer)),
		// Relay CLIENT: use relays others offer. Explicit even though it is the
		// default, because the default changing would silently remove our
		// fallback.
		libp2p.EnableRelay(),
		// NO DHT, NO mDNS, NO PUBLIC BOOTSTRAP PEERS, and this is a security
		// decision rather than a minimalism one. Joining a global DHT would
		// publish members' addresses to strangers and would make peer discovery a
		// thing this host does with the whole internet — §30.2 forbids discovery
		// from carrying authorization weight, and the least error-prone way to
		// honour that is to have no open discovery at all. Peers come from the
		// coordinator (Rendezvous) or they do not come.
		libp2p.DisableMetrics(),
	}
	if o.ForceReachability != nil {
		switch *o.ForceReachability {
		case network.ReachabilityPublic:
			libopts = append(libopts, libp2p.ForceReachabilityPublic())
		case network.ReachabilityPrivate:
			libopts = append(libopts, libp2p.ForceReachabilityPrivate())
		}
	}
	if limits.OfferRelayService {
		// "Run those relays on publicly dialable donors" (HARDENING-PLAN line
		// 901). libp2p only actually starts the service once AutoNAT says we are
		// publicly reachable, which is the same condition, and the owner's
		// ceilings are handed straight to relayv2's own accounting.
		libopts = append(libopts, libp2p.EnableRelayService(relayv2.WithResources(limits.RelayResources())))
		// EnableNATService lets us answer other peers' AutoNAT probes. It is tied
		// to relay duty on purpose: a host volunteering to carry bytes is already
		// volunteering to be a good citizen, and a host that is not should not be
		// dialling arbitrary peers back.
		libopts = append(libopts, libp2p.EnableNATService())
	}

	h, err := libp2p.New(libopts...)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Host{
		opts: o, enabled: true, limits: limits,
		host: h, gater: g, ledger: ledger, punch: tracer,
		cancel: cancel, reach: network.ReachabilityUnknown,
		reservations: make(map[peer.ID]relayReservation),
	}
	if o.ForceReachability != nil {
		p.reach, p.reachSeen = *o.ForceReachability, true
	}

	sub, err := h.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		cancel()
		_ = h.Close()
		return nil, err
	}
	p.wg.Add(2)
	go p.watchReachability(ctx, sub)
	// The time ceiling needs a ticker, not an I/O hook: the circuit this cap
	// exists for is the one that has stopped doing I/O. See Ledger.EnforceTime.
	go p.enforceTime(ctx)
	return p, nil
}

// Enabled reports the feature flag.
func (p *Host) Enabled() bool { return p.enabled }

// PeerID is this host's libp2p peer ID, empty when disabled.
func (p *Host) PeerID() peer.ID {
	if !p.enabled {
		return ""
	}
	return p.host.ID()
}

// DeviceID is this host's pool fingerprint. It is computed from the pool identity
// rather than from the peer ID so that a bug in the mapping shows up as a
// mismatch a test can catch, not as two consistent-but-wrong values.
func (p *Host) DeviceID() string { return pool.DeviceID(p.opts.Identity.PublicKey) }

// Addrs are the multiaddrs this host is listening on.
func (p *Host) Addrs() []ma.Multiaddr {
	if !p.enabled {
		return nil
	}
	return p.host.Addrs()
}

// Libp2pHost exposes the underlying host for the agent to register handlers and
// for tests. It returns nil when disabled, which is why every caller must check.
func (p *Host) Libp2pHost() libhost.Host {
	if !p.enabled {
		return nil
	}
	return p.host
}

func (p *Host) watchReachability(ctx context.Context, sub event.Subscription) {
	defer p.wg.Done()
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.Out():
			if !ok {
				return
			}
			evt, ok := e.(event.EvtLocalReachabilityChanged)
			if !ok {
				continue
			}
			p.mu.Lock()
			p.reach = evt.Reachability
			// reachSeen distinguishes "AutoNAT has not spoken" from "AutoNAT said
			// unknown". They lead to different decisions, so they are different
			// states. See Dialability.AutoNATObserved.
			p.reachSeen = true
			p.mu.Unlock()
		}
	}
}

// timeEnforcementInterval is how often expired circuits are reaped. It is short
// relative to the smallest sane MaxCircuitSeconds, so the effective ceiling is
// the owner's value plus at most this, and never less than it — a cap that fired
// early would look like a random transfer failure.
const timeEnforcementInterval = 5 * time.Second

func (p *Host) enforceTime(ctx context.Context) {
	defer p.wg.Done()
	t := time.NewTicker(timeEnforcementInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.ledger.EnforceTime()
		}
	}
}

// Dialability is the reconciled AutoNAT + STUN view. See nat.go for why these are
// composed rather than ranked.
func (p *Host) Dialability() Dialability {
	if !p.enabled {
		d := Reconcile(network.ReachabilityUnknown, false, p.opts.STUN)
		d.Summary = "the libp2p data plane is disabled, so AutoNAT has measured nothing; " + d.Summary
		return d
	}
	p.mu.Lock()
	r, seen := p.reach, p.reachSeen
	p.mu.Unlock()
	return Reconcile(r, seen, p.opts.STUN)
}

// Publish advertises this host's peer ID and bound addresses through the
// rendezvous seam. It is a no-op when disabled — a disabled host must not leak an
// address, which is the same position Phase 1 took for the reflexive address.
func (p *Host) Publish(ctx context.Context) error {
	if !p.enabled {
		return ErrDisabled
	}
	if p.opts.Rendezvous == nil {
		return ErrNoRendezvous
	}
	addrs := make([]string, 0, 8)
	for _, a := range p.host.Addrs() {
		addrs = append(addrs, a.String())
	}
	// Relay reservation addresses are published too: they are how a peer behind a
	// symmetric NAT is reachable at all, and a reservation nobody knows about is a
	// reservation that buys nothing.
	p.mu.Lock()
	for id, res := range p.reservations {
		for _, a := range res.addrs {
			addrs = append(addrs, a.String()+"/p2p/"+id.String()+"/p2p-circuit")
		}
	}
	p.mu.Unlock()
	sort.Strings(addrs)
	return p.opts.Rendezvous.Publish(ctx, PeerRecord{
		DeviceID: p.DeviceID(), PeerID: p.host.ID().String(), Addrs: addrs,
	})
}

// Reserve takes a Circuit Relay v2 reservation on a relay candidate.
//
// The relay must itself be an AUTHORIZED peer. That is not belt-and-braces: a
// relay sees connection metadata for every circuit it carries, so accepting
// reservations from an arbitrary relay would hand traffic analysis to a stranger
// and would let an unauthorized host insert itself into the path.
func (p *Host) Reserve(ctx context.Context, relay peer.AddrInfo) error {
	if !p.enabled {
		return ErrDisabled
	}
	device, err := DeviceIDFromPeerID(relay.ID)
	if err != nil {
		return err
	}
	if !p.opts.Authorizer.Authorized(device) {
		return ErrUnauthorized
	}
	if err := p.host.Connect(ctx, relay); err != nil {
		return err
	}
	res, err := relayclient.Reserve(ctx, p.host, relay)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.reservations[relay.ID] = relayReservation{
		Expiration: res.Expiration, LimitDuration: res.LimitDuration, LimitData: res.LimitData,
		addrs: p.host.Peerstore().Addrs(relay.ID),
	}
	p.mu.Unlock()
	// Listening on /p2p-circuit is what turns a reservation into an address peers
	// can actually dial; Reserve alone only books the slot.
	circuit, err := ma.NewMultiaddr("/p2p-circuit")
	if err != nil {
		return err
	}
	if err := p.host.Network().Listen(circuit); err != nil {
		// A reservation without a circuit listener is useless but not harmful, and
		// the reservation is already recorded, so report rather than unwind.
		return err
	}
	return nil
}

// Connect establishes a connection to an authorized peer by device fingerprint,
// preferring the direct path and falling back to a relayed circuit.
//
// LAN IS PREFERRED AND THIS FUNCTION DOES NOT COMPETE WITH IT. The caller is
// expected to have tried the existing internal/pool mutual-TLS path first; the
// LAN path is the proven transport and nothing here replaces it. If the peer's
// addresses are all private, this returns ErrInvalid with the intent that the
// caller use the LAN path instead of tunnelling a LAN peer through libp2p for no
// reason. Snapshot.PreferLAN reports how often that happened.
func (p *Host) Connect(ctx context.Context, deviceID string) (peer.ID, bool, error) {
	if !p.enabled {
		return "", false, ErrDisabled
	}
	if !validFingerprint(deviceID) {
		return "", false, ErrInvalid
	}
	// AUTHORIZATION BEFORE ADDRESSING, not after. Resolving first would mean
	// asking the coordinator about a device we are not allowed to talk to, which
	// is a membership oracle.
	if !p.opts.Authorizer.Authorized(deviceID) {
		return "", false, ErrUnauthorized
	}
	if p.opts.Rendezvous == nil {
		return "", false, ErrNoRendezvous
	}
	info, err := p.opts.Rendezvous.Peer(ctx, deviceID)
	if err != nil {
		return "", false, err
	}
	// Re-derive the fingerprint from the peer ID the coordinator returned. The
	// coordinator is supposed to enforce this pairing; trusting that it did would
	// make the coordinator a second source of truth about identity.
	got, err := DeviceIDFromPeerID(info.ID)
	if err != nil || got != deviceID {
		return "", false, ErrUnauthorized
	}
	allPrivate := len(info.Addrs) > 0
	for _, a := range info.Addrs {
		if !PrivateAddr(a) {
			allPrivate = false
			break
		}
	}
	if allPrivate {
		p.punch.countLANPreferred()
		return "", false, ErrInvalid
	}
	if err := p.host.Connect(ctx, info); err != nil {
		return "", false, err
	}
	relayed := true
	for _, c := range p.host.Network().ConnsToPeer(info.ID) {
		if !Relayed(c.RemoteMultiaddr()) {
			relayed = false
			break
		}
	}
	return info.ID, relayed, nil
}

// OpenStream opens the nexal transfer stream to an already-connected peer and
// meters it IF AND ONLY IF the connection is relayed.
//
// The conditional is the cost control's shape: a direct connection costs nobody
// anything, so metering it would throttle the good path. A relayed connection
// spends a donor's bandwidth, so every byte is charged.
func (p *Host) OpenStream(ctx context.Context, id peer.ID) (network.Stream, error) {
	if !p.enabled {
		return nil, ErrDisabled
	}
	device, err := DeviceIDFromPeerID(id)
	if err != nil {
		return nil, err
	}
	// Checked again at stream time, not only at connect time. A peer authorized
	// when the connection opened may have been revoked since, and a long-lived
	// connection must not outlive its authorization.
	if !p.opts.Authorizer.Authorized(device) {
		return nil, ErrUnauthorized
	}
	// libp2p marks relayed connections LIMITED and refuses to open a stream on one
	// unless the caller explicitly opts in. That default is the same posture this
	// package takes — the relay is the expensive fallback — so the opt-in is scoped
	// to the case where the ONLY path is relayed, and it is paired with the meter
	// below so opting in also means opting into the byte ceiling.
	if p.onlyRelayed(id) {
		ctx = network.WithAllowLimitedConn(ctx, "nexal relayed fallback, metered against the owner's byte ceiling")
	}
	s, err := p.host.NewStream(ctx, id, Protocol)
	if err != nil {
		return nil, err
	}
	return p.wrap(s)
}

// onlyRelayed reports that every connection we hold to this peer traverses a
// relay, i.e. there is no direct path to prefer.
func (p *Host) onlyRelayed(id peer.ID) bool {
	conns := p.host.Network().ConnsToPeer(id)
	if len(conns) == 0 {
		return false
	}
	for _, c := range conns {
		if !Relayed(c.RemoteMultiaddr()) {
			return false
		}
	}
	return true
}

// Handle registers the inbound handler for the transfer protocol, applying the
// same authorization re-check and the same metering rule as OpenStream.
func (p *Host) Handle(fn func(network.Stream)) error {
	if !p.enabled {
		return ErrDisabled
	}
	p.host.SetStreamHandler(Protocol, func(s network.Stream) {
		device, err := DeviceIDFromPeerID(s.Conn().RemotePeer())
		if err != nil || !p.opts.Authorizer.Authorized(device) {
			// The gater should already have refused this peer. Refusing again
			// here is cheap and closes the window where a peer was revoked after
			// its connection was established.
			_ = s.Reset()
			return
		}
		wrapped, err := p.wrap(s)
		if err != nil {
			_ = s.Reset()
			return
		}
		fn(wrapped)
	})
	return nil
}

// wrap applies the relay meter. A direct stream is returned unwrapped so the fast
// path has no added indirection at all.
func (p *Host) wrap(s network.Stream) (network.Stream, error) {
	if !Relayed(s.Conn().RemoteMultiaddr()) {
		return s, nil
	}
	c, err := p.ledger.Open(s.Conn().RemotePeer(), s.Conn())
	if err != nil {
		_ = s.Reset()
		return nil, err
	}
	return Meter(s, c), nil
}

// Consumption is the surfaced ceiling usage, valid (and zeroed) when disabled.
func (p *Host) Consumption() Consumption {
	if !p.enabled {
		c := Consumption{Limits: p.limits, Note: "the libp2p data plane is disabled; no relay bytes can be spent"}
		return c
	}
	return p.ledger.Consumption()
}

// Snapshot is the whole owner-facing state in one value.
type Snapshot struct {
	Enabled       bool        `json:"enabled"`
	PeerID        string      `json:"peerId,omitempty"`
	DeviceID      string      `json:"deviceId,omitempty"`
	Addrs         []string    `json:"addrs,omitempty"`
	Dialability   Dialability `json:"dialability"`
	Consumption   Consumption `json:"consumption"`
	Reservations  int         `json:"relayReservations"`
	RelayService  bool        `json:"offeringRelayService"`
	Punches       PunchStats  `json:"holePunching"`
	Denials       []Denial    `json:"recentDenials,omitempty"`
	DeniedTotal   uint64      `json:"deniedTotal"`
	AuthorizedNum int         `json:"authorizedPeers"`
	// PreferLAN counts times Connect refused because the peer was reachable on a
	// private address, i.e. the existing LAN path should carry it. A high number
	// is good news, not an error rate.
	PreferLAN uint64 `json:"lanPreferred"`
	Note      string `json:"note"`
}

func (p *Host) Snapshot() Snapshot {
	s := Snapshot{
		Enabled:     p.enabled,
		Dialability: p.Dialability(),
		Consumption: p.Consumption(),
		Note: "libp2p is an ADDITIONAL path for peers no private address can reach; the LAN mutual-TLS path in internal/pool is preferred and unchanged. " +
			"Reachability is not authorization: every peer here was checked against the coordinator's fingerprint allowlist (HARDENING-PLAN §30.2)",
	}
	if !p.enabled {
		return s
	}
	s.PeerID = p.host.ID().String()
	s.DeviceID = p.DeviceID()
	for _, a := range p.host.Addrs() {
		s.Addrs = append(s.Addrs, a.String())
	}
	p.mu.Lock()
	s.Reservations = len(p.reservations)
	p.mu.Unlock()
	s.RelayService = p.limits.OfferRelayService
	s.Punches = p.punch.Stats()
	s.PreferLAN = s.Punches.LANPreferred
	s.Denials, s.DeniedTotal = p.gater.Denials()
	if p.opts.Authorizer != nil {
		s.AuthorizedNum = len(p.opts.Authorizer.Snapshot())
	}
	return s
}

// Close shuts the host down. It is idempotent and safe on a disabled Host.
func (p *Host) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	if !p.enabled {
		return nil
	}
	if p.opts.Rendezvous != nil {
		// Withdraw the published addresses: a stale address set is a peer
		// repeatedly failing to dial a host that is gone.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.opts.Rendezvous.Withdraw(ctx)
		cancel()
	}
	p.cancel()
	err := p.host.Close()
	p.wg.Wait()
	return err
}

func multiaddrOf(s string) (ma.Multiaddr, error) { return ma.NewMultiaddr(s) }
