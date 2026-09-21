package agent

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/discovery"
)

// Peer discovery, wired into the agent lifecycle.
//
// Three loops, all optional and all gated, and one rule that shapes every line
// below: DISCOVERY NEVER AUTHORIZES.
//
//	advertise    POST /api/peers/advertise, on start and on an interval, so the
//	             coordinator's host_addresses row for this host is not empty and
//	             peers reading the directory see somewhere to dial.
//	rendezvous   GET /api/peers on an interval, via discovery.Rendezvous, which
//	             retains the last known set when the coordinator is unreachable
//	             rather than emptying an allowlist mid-transfer.
//	mDNS         the local-link responder/browser, when the LAN gate is open.
//
// The only input to AllowedPeers is the coordinator's set. mDNS candidates are
// merged for display, locality and dial addresses, and discovery.Merge plus
// discovery.AllowedPeers keep that asymmetry; nothing here re-derives an
// allowlist. Authority remains the mutual-TLS peerPolicy in internal/pool, which
// pins an exact ed25519 fingerprint on every handshake.

const (
	// advertiseInterval republishes addresses often enough that a DHCP change or
	// a new interface is visible to peers within a few minutes, and rarely
	// enough that it is not a second heartbeat. Advertise is a full replacement
	// of this host's published set, so each run is idempotent.
	advertiseInterval = 5 * time.Minute
	// advertiseRetryInterval is the shorter wait after a failed advertise: until
	// one succeeds, this host has no published address at all, so the first
	// successful publish matters more than politeness. It is still slow enough
	// not to become a retry storm against an unreachable coordinator.
	advertiseRetryInterval = 45 * time.Second
	// mergeInterval recomputes the merged candidate view. It is local work only:
	// no request is made, the coordinator poll has its own jittered schedule
	// inside discovery.Rendezvous.
	mergeInterval = 15 * time.Second
	// browseInterval re-queries the local link. mDNS records are short-lived by
	// design, because a stale SRV points a dial at whoever holds the address now.
	browseInterval = 60 * time.Second
	// maxLANCandidates bounds what one hostile local network can make this
	// process retain. A café LAN may announce as many instances as it likes.
	maxLANCandidates = 128
	// candidateTTL drops a LAN candidate that has not been heard from. It is
	// longer than browseInterval so one lost query does not clear the view.
	candidateTTL = 5 * time.Minute
)

// PeerPublisher is the coordinator write that makes the peer directory useful.
// It is an interface so the agent can be tested without a network, and so the
// agent does not gain a second HTTP client: *client.Client satisfies it.
type PeerPublisher interface {
	Advertise(ctx context.Context, fingerprint string, addresses []client.LANAddress,
		capabilities *client.PeerCapabilities) (client.AdvertiseAck, error)
}

// DiscoveryOptions wires the discovery package into this agent. Publisher and
// Source are normally the same *client.Client.
type DiscoveryOptions struct {
	Config  *config.Discovery
	Sharing discovery.SharingPolicy
	// Publisher performs the advertise write; nil disables publishing, which
	// means this host stays invisible to its peers rather than half-published.
	Publisher PeerPublisher
	// Source is the coordinator's peer directory read.
	Source discovery.PeerSource
	// Capabilities is this host's self-report. It is self-reported and attested
	// by nothing, at every layer, and may never alone open a gated path.
	Capabilities *client.PeerCapabilities
	// StaticPeers are the owner-configured cross-VLAN endpoints
	// (config.StaticPeers). They contribute dial addresses to the merged view and
	// authorize nothing — discovery.MergeStatic marks them Configured and never
	// sets Authorized, so AllowedPeers is unaffected by their presence.
	StaticPeers []discovery.Static
	// LocalAddresses is the interface enumeration, injectable for tests. Nil
	// uses the host's real interfaces.
	LocalAddresses func() []netip.Addr
	// Clock is injectable for tests; nil is time.Now.
	Clock func() time.Time
	// AdvertiseInterval and MergeInterval override the defaults above. They exist
	// so tests can observe a refresh without waiting minutes; zero takes the
	// documented default, and a production caller passes neither.
	AdvertiseInterval time.Duration
	MergeInterval     time.Duration
}

// WithDiscovery installs peer discovery. It is an Option rather than a parameter
// of New because discovery is off unless an owner turned it on: a connector with
// no discovery block in its config behaves exactly as it did before.
func WithDiscovery(o DiscoveryOptions) Option {
	return func(a *Agent) { a.discovery = &o }
}

// PeerView is the agent's last merged candidate view, for status surfaces.
type PeerView struct {
	// Peers is the merged view. Peer.Authorized is true only for peers the
	// coordinator listed.
	Peers []discovery.Peer
	// Allowed is the exact fingerprint allowlist for pool.PeerOptions, derived
	// from the coordinator's set alone.
	Allowed []string
	// Directory carries the coordinator read's staleness and last error.
	Directory discovery.Snapshot
	// LastAdvertisedAt is when this host last successfully published its
	// addresses; zero means it never has, which is not the same as "no
	// addresses" and is reported separately for that reason.
	LastAdvertisedAt time.Time
	// ObservedWANAddress is what the coordinator saw the last advertise arrive
	// from. Empty means no edge address was present (local development), not
	// that this host has no public address.
	ObservedWANAddress string
	// LastAdvertiseError is the client package's already-scrubbed message.
	LastAdvertiseError string
}

// PeerCandidates returns the last merged view. It is a copy: callers cannot
// mutate the agent's state, and in particular cannot append to Allowed.
func (a *Agent) PeerCandidates() PeerView {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.peerView
	v.Peers = append([]discovery.Peer(nil), a.peerView.Peers...)
	v.Allowed = append([]string(nil), a.peerView.Allowed...)
	return v
}

// runDiscovery runs the configured discovery loops until ctx is done. It returns
// immediately when discovery is not configured or every gate is shut: a closed
// gate is not an error, it is the default.
func (a *Agent) runDiscovery(ctx context.Context) {
	o := a.discovery
	gated := o != nil && (o.Sharing.LANDiscovery || o.Sharing.WANRendezvous)
	// Static peers are merged even with both discovery gates shut: the owner named
	// those addresses explicitly, and refusing to show them because multicast is
	// off would hide the one part of the view that does not depend on multicast.
	// Nothing is announced or polled in that state, and nothing is authorized.
	if o == nil || (!gated && len(o.StaticPeers) == 0) {
		return
	}
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}
	fingerprint := ""
	var port uint16
	if o.Config != nil {
		fingerprint, port = o.Config.DeviceFingerprint, o.Config.PeerPort
	}
	if gated && (!discovery.ValidFingerprint(fingerprint) || port == 0) {
		// config.Discovery.Validate already refuses this combination, so reaching
		// here means a caller built options by hand. Fail closed and say so once.
		// Only the announcing/polling paths need this host's own identity; a static
		// peer list does not, which is why the check is scoped to an open gate.
		a.logger.Warn("peer discovery not started: a device fingerprint and peer port are required")
		return
	}

	var rendezvous *discovery.Rendezvous
	if o.Sharing.WANRendezvous && o.Source != nil {
		r, err := discovery.NewRendezvous(discovery.RendezvousOptions{
			Source: o.Source, Sharing: o.Sharing, Clock: clock})
		if err != nil {
			a.logger.Warn("coordinator peer directory poll not started", "error", errorText(err))
		} else {
			rendezvous = r
			go r.Run(ctx)
		}
	}

	lan := newCandidateSet(clock)
	if o.Sharing.LANDiscovery {
		// A refused multicast join is ordinary — a VPN-only interface, a Mac with
		// IPv6 disabled, a sandbox with no multicast route. WAN rendezvous still
		// works without it, so this is logged and not fatal.
		m, err := discovery.Listen(discovery.Options{
			Sharing: o.Sharing,
			Advertisement: discovery.Advertisement{
				HostID: a.Snapshot().HostID, Fingerprint: fingerprint,
				Version: config.Version, Port: port,
				Addresses: o.localAddresses(),
			},
			Clock: clock, Logger: a.logger,
		})
		if err != nil {
			a.logger.Warn("LAN discovery not started", "error", errorText(err))
		} else {
			defer m.Close()
			go m.Run(ctx, lan.add)
			go func() {
				t := time.NewTicker(browseInterval)
				defer t.Stop()
				for {
					if err := m.Browse(); err != nil {
						a.logger.Debug("LAN browse failed", "error", errorText(err))
					}
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
		}
	}

	if o.Sharing.WANRendezvous && o.Publisher != nil {
		go a.advertiseLoop(ctx, fingerprint, port)
	}

	link, err := discovery.LocalLinkView()
	if err != nil {
		// Without a link view every locality is Unknown, which grants nothing and
		// loses only a transport hint. It is not a reason to stop discovering.
		a.logger.Debug("local link view unavailable", "error", errorText(err))
	}
	t := time.NewTicker(o.mergeEvery())
	defer t.Stop()
	for {
		a.mergePeers(rendezvous, lan, link, clock())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// mergePeers recomputes the merged view. The allowlist is produced by
// discovery.AllowedPeers, which reads Peer.Authorized and nothing a multicast
// packet can influence.
func (a *Agent) mergePeers(r *discovery.Rendezvous, lan *candidateSet, link discovery.LinkView, now time.Time) {
	var snapshot discovery.Snapshot
	if r != nil {
		snapshot = r.Snapshot()
	}
	var static []discovery.Static
	if a.discovery != nil {
		static = a.discovery.StaticPeers
	}
	peers := discovery.MergeStatic(snapshot, lan.list(now), static, link, now)
	allowed := discovery.AllowedPeers(peers)
	a.mu.Lock()
	a.peerView.Peers, a.peerView.Allowed, a.peerView.Directory = peers, allowed, snapshot
	a.mu.Unlock()
}

// advertiseLoop publishes this host's fingerprint and lan addresses immediately,
// then on an interval. Until one publish succeeds the coordinator's
// host_addresses row for this host is empty, and every peer sees this host with
// no address at all — which is why a failure retries sooner than a success.
func (a *Agent) advertiseLoop(ctx context.Context, fingerprint string, port uint16) {
	for {
		wait := a.discovery.advertiseEvery()
		if err := a.advertise(ctx, fingerprint, port); err != nil {
			if ctx.Err() != nil {
				return
			}
			wait = advertiseRetryInterval
			if every := a.discovery.advertiseEvery(); every < wait {
				// A caller that asked for a faster cadence than the retry wait
				// meant it; never retry slower than the configured interval.
				wait = every
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *Agent) advertise(ctx context.Context, fingerprint string, port uint16) error {
	o := a.discovery
	addresses := advertisableAddresses(o.localAddresses(), port)
	reqCtx, stop := boundedRequest(ctx, time.Time{})
	defer stop()
	ack, err := o.Publisher.Advertise(reqCtx, fingerprint, addresses, o.Capabilities)
	a.mu.Lock()
	if err != nil {
		a.peerView.LastAdvertiseError = errorText(err)
	} else {
		a.peerView.LastAdvertisedAt = time.Now()
		a.peerView.ObservedWANAddress = ack.ObservedWANAddress
		a.peerView.LastAdvertiseError = ""
	}
	a.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			a.logger.Debug("peer advertise abandoned during shutdown")
			return err
		}
		a.logger.Warn("peer advertise failed", "error", errorText(err))
		return err
	}
	// Published is not authorized: this records that the coordinator holds a
	// candidate list entry for this host, nothing more.
	a.logger.Info("advertised peer candidates",
		"lan_addresses", ack.LANAddresses, "wan_observed", ack.ObservedWANAddress != "",
		"authorization", ack.Authorization)
	return nil
}

func (o *DiscoveryOptions) advertiseEvery() time.Duration {
	if o.AdvertiseInterval > 0 {
		return o.AdvertiseInterval
	}
	return advertiseInterval
}

func (o *DiscoveryOptions) mergeEvery() time.Duration {
	if o.MergeInterval > 0 {
		return o.MergeInterval
	}
	return mergeInterval
}

// localAddresses enumerates this host's addresses, through the injected hook
// when tests supply one.
func (o *DiscoveryOptions) localAddresses() []netip.Addr {
	if o.LocalAddresses != nil {
		return o.LocalAddresses()
	}
	return localInterfaceAddresses()
}

// advertisableAddresses filters interface addresses to the ones the coordinator
// accepts as lan candidates and caps the list at the server's cap.
//
// Filtering happens here rather than being discovered from an HTTP 400 because
// advertise is an atomic replacement: one unacceptable entry refuses the whole
// request, which would leave the previous address set published while this host
// believed it had republished.
//
// The order is sorted and then truncated, so which eight addresses a machine
// with many interfaces publishes is stable across runs rather than dependent on
// interface enumeration order.
func advertisableAddresses(addrs []netip.Addr, port uint16) []client.LANAddress {
	if port == 0 {
		return nil
	}
	seen := map[string]bool{}
	claims := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		normalized, ok := client.ValidLANAddressClaim(addr.Unmap().WithZone("").String())
		if !ok || seen[normalized] {
			continue
		}
		seen[normalized] = true
		claims = append(claims, normalized)
	}
	sort.Strings(claims)
	if len(claims) > client.MaxAdvertisedLANAddresses {
		claims = claims[:client.MaxAdvertisedLANAddresses]
	}
	out := make([]client.LANAddress, 0, len(claims))
	for _, address := range claims {
		out = append(out, client.LANAddress{Kind: "lan", Address: address, Port: port})
	}
	return out
}

// localInterfaceAddresses reads unicast addresses from up, non-loopback
// interfaces. Point-to-point interfaces are skipped for the same reason
// discovery.LocalLinkView skips them: that is the shape a macOS utun VPN takes,
// and publishing a VPN address invites peers to dial across a tunnel that may
// not carry them.
func localInterfaceAddresses() []netip.Addr {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			out = append(out, addr.Unmap())
			if len(out) >= 64 {
				return out
			}
		}
	}
	return out
}

// candidateSet holds what the local link claimed, bounded and expiring.
//
// Everything in here is unauthenticated input from whoever is on the network.
// It is bounded so a hostile LAN cannot grow this process's memory, and expiring
// so a machine that left the link stops being offered as a dial candidate.
type candidateSet struct {
	now func() time.Time

	mu    sync.Mutex
	items map[string]discovery.Candidate
}

func newCandidateSet(now func() time.Time) *candidateSet {
	return &candidateSet{now: now, items: map[string]discovery.Candidate{}}
}

// add is the mDNS sink. It must not block: it is called from the read goroutines.
func (s *candidateSet) add(found []discovery.Candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range found {
		if !discovery.ValidFingerprint(c.Fingerprint) || !discovery.ValidHostID(c.HostID) {
			continue
		}
		if _, known := s.items[c.Fingerprint]; !known && len(s.items) >= maxLANCandidates {
			// Full. Dropping the new claim rather than evicting an existing one
			// means a flood cannot displace the peers already found.
			continue
		}
		s.items[c.Fingerprint] = c
	}
}

// list returns the unexpired candidates.
func (s *candidateSet) list(now time.Time) []discovery.Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]discovery.Candidate, 0, len(s.items))
	for fingerprint, c := range s.items {
		if !c.SeenAt.IsZero() && now.Sub(c.SeenAt) > candidateTTL {
			delete(s.items, fingerprint)
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

// StaticPeersFrom converts the owner's configured endpoints into dial candidates.
//
// The conversion is lossy on purpose: what crosses into the discovery package is
// an address, a port and the fingerprint to pin. The endpoint STRING stays in
// internal/config, where pool.ValidPeerEndpoint validated it, so no other
// package can reconstruct a URL that skipped that check. Entries that fail to
// parse are dropped rather than passed through half-formed; config.Validate
// already refused them at load, so this is defence in depth, not a code path an
// owner can reach.
func StaticPeersFrom(peers []config.StaticPeer) []discovery.Static {
	out := make([]discovery.Static, 0, len(peers))
	for _, p := range peers {
		addr, ok := p.AddrPort()
		if !ok || !discovery.ValidFingerprint(p.Fingerprint) {
			continue
		}
		out = append(out, discovery.Static{Fingerprint: p.Fingerprint, Address: addr, Label: p.Label})
	}
	return out
}
