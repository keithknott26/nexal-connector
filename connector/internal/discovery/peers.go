package discovery

import (
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// The merged view of "which Macs might be reachable", and the one rule that this
// whole package exists to preserve:
//
//	Discovery never authorizes.
//
// mDNS and rendezvous both produce candidates. The authority on whether a peer
// may read or write a Store is the mutual-TLS peerPolicy in internal/pool, which
// pins an exact ed25519 device fingerprint. An mDNS TXT record is an
// unauthenticated claim from whoever is on the local network; it may select which
// fingerprint we *attempt*, and it may never add a fingerprint to AllowedPeers.
// AllowedPeers below is derived from the coordinator's authorized set only, and
// TestMDNSOnlyPeerNeverEntersAllowedPeers asserts it.

// Source records where a peer became known. It is provenance, not permission.
type Source uint8

const (
	SourceMDNS Source = iota + 1
	SourceRendezvous
	SourceBoth
	// SourceStatic is an owner-typed endpoint from the config file. It is
	// provenance only, exactly like the other three: see static.go. It is listed
	// separately from SourceBoth rather than folded into it so a UI can say
	// "statically configured" instead of implying the peer was discovered.
	SourceStatic
)

func (s Source) String() string {
	switch s {
	case SourceMDNS:
		return "mdns"
	case SourceRendezvous:
		return "rendezvous"
	case SourceBoth:
		return "both"
	case SourceStatic:
		return "static"
	}
	return "unknown"
}

// Locality is a transport hint and grants nothing.
//
// Subnet equality is explicitly not the test (HARDENING-PLAN §35.5): a VPN hands
// out RFC1918 addresses, so two machines in different buildings can share a
// 10.x /24 and look adjacent (§34.3). Classification therefore matches the peer
// address against this host's own interface prefixes, skips point-to-point and
// tunnel-shaped interfaces, and reserves SameLink for the one piece of evidence
// that is hard to fake across a router: having heard the peer over multicast.
// When the evidence is ambiguous the answer is Unknown, because a wrong SameLink
// is the claim that invites a fast path, and a fast path that skips checks is how
// a café network becomes a data breach.
type Locality uint8

const (
	LocalityUnknown Locality = iota
	LocalitySameLink
	LocalityLAN
	LocalityWAN
)

func (l Locality) String() string {
	switch l {
	case LocalitySameLink:
		return "same-link"
	case LocalityLAN:
		return "lan"
	case LocalityWAN:
		return "wan"
	}
	return "unknown"
}

// rank orders localities by preference for a transport hint. Unknown is last.
func (l Locality) rank() int {
	switch l {
	case LocalitySameLink:
		return 0
	case LocalityLAN:
		return 1
	case LocalityWAN:
		return 2
	}
	return 3
}

// Peer is one row of the merged view. It is a display and transport-planning
// record. Authorized is the only field that carries weight, and it is set from
// the coordinator's list alone.
type Peer struct {
	HostID      string   `json:"hostId"`
	Name        string   `json:"name,omitempty"`
	Fingerprint string   `json:"fingerprint"`
	Source      Source   `json:"-"`
	Locality    Locality `json:"-"`
	// Authorized is true only if the coordinator listed this peer. A peer that
	// is false here is displayable as "found, not authorized" and is never dialed
	// with credentials.
	Authorized bool `json:"authorized"`
	// AuthorizationStale is true when the coordinator's list is the last known
	// one rather than a fresh read. It is surfaced rather than resolved: dropping
	// a peer mid-transfer because the control plane blipped is worse than saying
	// the list is old.
	AuthorizationStale bool `json:"authorizationStale"`
	// Configured is true when an owner typed this peer's endpoint into the config
	// (config.StaticPeers) rather than it being discovered. It is LABELLING, not
	// permission: it is never read by AllowedPeers, and a surface must show a
	// configured peer as configured rather than as found on the network, because
	// "my Mac can see it" and "I told it where to look" are different facts.
	Configured bool `json:"configured"`
	// Addresses are dial candidates in preference order. They are hints; the
	// peer TLS handshake decides whether whoever answers is the right machine.
	Addresses []netip.AddrPort `json:"addresses,omitempty"`
	// Capabilities are self-reported and unattested, by both transports. The
	// field name says so on purpose; PlanMLX already treats them as necessary,
	// never sufficient.
	SelfReportedCapabilities Capabilities `json:"selfReportedCapabilities"`
	SeenOnLAN                bool         `json:"seenOnLan"`
	LastSeen                 time.Time    `json:"lastSeen,omitempty"`
	// Version is whatever the peer claimed. Useful for skew diagnosis, trusted
	// for nothing.
	Version string `json:"version,omitempty"`
}

// SourceLabel and LocalityLabel exist so a JSON surface can carry the strings
// without the enums leaking their numeric values into a wire format.
func (p Peer) SourceLabel() string   { return p.Source.String() }
func (p Peer) LocalityLabel() string { return p.Locality.String() }

// SharingPolicy gates the mechanisms in this package.
//
// Default-on resource sharing (HARDENING-PLAN §36.4) is an unresolved founder
// decision and is deliberately not decided here. Every field defaults to false,
// the zero value is the closed state, and DefaultSharing() returns it explicitly
// so that reading the code answers "what happens if nobody configures this?"
// without having to reason about struct zeroing.
type SharingPolicy struct {
	// LANDiscovery gates joining the mDNS groups at all — both advertising this
	// host and browsing for others.
	LANDiscovery bool `json:"lanDiscovery"`
	// WANRendezvous gates polling the coordinator's peer list.
	WANRendezvous bool `json:"wanRendezvous"`
	// ResourceSharing gates offering this machine's CPU/GPU/RAM to authorized
	// peers. It is the field §36.4 is actually about. It stays false here.
	ResourceSharing bool `json:"resourceSharing"`
}

// DefaultSharing is the shipped default: everything off, nothing announced,
// nothing offered. Turning any of it on is a decision made outside this package.
func DefaultSharing() SharingPolicy { return SharingPolicy{} }

// ValidFingerprint matches pool.DeviceID's output exactly: 64 lowercase hex
// characters. Uppercase is rejected rather than normalized, so that the value
// compared here is byte-identical to the value the peer TLS pins.
func ValidFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidHostID accepts the coordinator's host identifier shape, additionally
// bounded to one DNS label because it is used as the DNS-SD instance name.
func ValidHostID(s string) bool {
	if len(s) < 1 || len(s) > maxLabel {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
			!(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// validTXTValue bounds a TXT value and refuses control characters, so a decoded
// value cannot smuggle a newline into a log line or a terminal escape into a UI.
func validTXTValue(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// localAddress is the same shape as pool.privateIP, kept separate on purpose:
// this package must not be able to loosen that function by editing it.
func localAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || cgnat(addr)
}

// cgnat covers 100.64.0.0/10, which carriers and some mesh VPNs hand out. It is
// not "private" to netip, but it is certainly not a public address to dial.
func cgnat(addr netip.Addr) bool {
	return addr.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}

// LinkView is this host's own idea of which prefixes are directly attached.
type LinkView struct {
	prefixes []netip.Prefix
}

// NewLinkView builds a view from explicit prefixes, which is what tests use and
// what a caller with better information than net.Interfaces should use.
func NewLinkView(prefixes []netip.Prefix) LinkView {
	out := make([]netip.Prefix, 0, len(prefixes))
	for _, p := range prefixes {
		if p.IsValid() {
			out = append(out, p.Masked())
		}
	}
	return LinkView{prefixes: out}
}

// LocalLinkView reads the host's interface prefixes with their real mask lengths.
//
// Point-to-point interfaces are excluded because that is the shape a macOS utun
// VPN takes, and a VPN's prefix is exactly the one that makes a remote machine
// look adjacent. This is a mitigation, not a solution: a VPN presenting a
// broadcast-style interface is indistinguishable from a switch here, which is why
// SameLink additionally requires multicast evidence.
func LocalLinkView() (LinkView, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return LinkView{}, err
	}
	var prefixes []netip.Prefix
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
			ones, _ := ipnet.Mask.Size()
			prefix, err := addr.Unmap().Prefix(ones)
			if err != nil {
				continue
			}
			prefixes = append(prefixes, prefix)
			if len(prefixes) >= 64 {
				return NewLinkView(prefixes), nil
			}
		}
	}
	return NewLinkView(prefixes), nil
}

func (l LinkView) attached(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range l.prefixes {
		if p.Addr().Is4() == addr.Is4() && p.Contains(addr) {
			return true
		}
	}
	return false
}

// Classify maps one address to a locality. multicastEvidence means this host
// actually received a multicast datagram from the address: mDNS does not cross a
// router, so it is the strongest cheap evidence of a shared link.
func (l LinkView) Classify(addr netip.Addr, multicastEvidence bool) Locality {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() || addr.IsLoopback() {
		return LocalityUnknown
	}
	if !localAddress(addr) {
		// A globally routable unicast address is a WAN hint. It is still only a
		// hint: NAT, CGNAT and mobile networks all lie about reachability.
		if addr.IsGlobalUnicast() {
			return LocalityWAN
		}
		return LocalityUnknown
	}
	if addr.IsLinkLocalUnicast() {
		// 169.254/16 and fe80::/10 are per-link by definition, but without
		// multicast evidence we may be looking at a stale record for a link we
		// are no longer on, and we cannot even tell which interface it belongs
		// to without a zone.
		if multicastEvidence {
			return LocalitySameLink
		}
		return LocalityUnknown
	}
	if !l.attached(addr) {
		// Private, but not inside any prefix this host holds: another site's
		// RFC1918 space, or a VPN we are not on. Prefer Unknown over a guess.
		return LocalityUnknown
	}
	if multicastEvidence {
		return LocalitySameLink
	}
	// Inside one of our prefixes, but only ever heard about through the
	// coordinator. Plausibly reachable locally; not established to be adjacent.
	return LocalityLAN
}

// Merge combines the coordinator's authorized list with what multicast claimed.
//
// The asymmetry is the point. A rendezvous entry contributes identity and
// authorization. An mDNS candidate contributes addresses and, for a peer the
// coordinator already authorized, SameLink evidence. An mDNS candidate that the
// coordinator did not list contributes a display row and nothing else: it is not
// authorized, and AllowedPeers will not contain it.
func Merge(authorized Snapshot, seen []Candidate, link LinkView, now time.Time) []Peer {
	byFingerprint := map[string]*Peer{}
	order := []string{}
	for _, a := range authorized.Peers {
		if !ValidFingerprint(a.Fingerprint) || !ValidHostID(a.HostID) {
			// The coordinator is trusted for authorization, not for well-formed
			// output; a row we cannot pin is a row we cannot dial.
			continue
		}
		p := &Peer{
			HostID: a.HostID, Name: a.Name, Fingerprint: a.Fingerprint,
			Source: SourceRendezvous, Authorized: true,
			AuthorizationStale:       authorized.Stale,
			SelfReportedCapabilities: a.SelfReportedCapabilities,
			Locality:                 LocalityUnknown,
		}
		for _, claim := range a.Addresses {
			addr, err := netip.ParseAddr(claim.Address)
			if err != nil || claim.Port == 0 {
				continue
			}
			p.Addresses = append(p.Addresses, netip.AddrPortFrom(addr.Unmap(), claim.Port))
			p.Locality = closer(p.Locality, link.Classify(addr, false))
		}
		if _, exists := byFingerprint[a.Fingerprint]; !exists {
			order = append(order, a.Fingerprint)
		}
		byFingerprint[a.Fingerprint] = p
	}
	for _, c := range seen {
		if !ValidFingerprint(c.Fingerprint) || !ValidHostID(c.HostID) || c.Port == 0 {
			continue
		}
		p := byFingerprint[c.Fingerprint]
		if p == nil {
			// Found on the LAN, not authorized by the coordinator. Displayable,
			// never dialable with credentials, never in AllowedPeers.
			p = &Peer{
				HostID: c.HostID, Name: c.HostID, Fingerprint: c.Fingerprint,
				Source: SourceMDNS, Authorized: false, Locality: LocalityUnknown,
			}
			byFingerprint[c.Fingerprint] = p
			order = append(order, c.Fingerprint)
		} else if p.Source == SourceRendezvous {
			p.Source = SourceBoth
		}
		p.SeenOnLAN = true
		p.Version = c.Version
		if c.SeenAt.After(p.LastSeen) {
			p.LastSeen = c.SeenAt
		}
		// The datagram's source address is the one address in a multicast claim
		// the sender could not invent, so it is the one that may raise locality
		// to SameLink. Addresses merely listed in the packet do not.
		if c.From.IsValid() {
			p.Addresses = appendAddress(p.Addresses, netip.AddrPortFrom(c.From, c.Port))
			p.Locality = closer(p.Locality, link.Classify(c.From, true))
		}
		for _, addr := range c.Addresses {
			p.Addresses = appendAddress(p.Addresses, netip.AddrPortFrom(addr.Unmap(), c.Port))
			p.Locality = closer(p.Locality, link.Classify(addr, false))
		}
	}
	out := make([]Peer, 0, len(order))
	for _, fp := range order {
		p := byFingerprint[fp]
		sort.SliceStable(p.Addresses, func(i, j int) bool {
			return link.Classify(p.Addresses[i].Addr(), false).rank() <
				link.Classify(p.Addresses[j].Addr(), false).rank()
		})
		out = append(out, *p)
	}
	// Authorized peers first, then by host id, so a hostile LAN cannot reorder
	// the list a user sees by choosing a name.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Authorized != out[j].Authorized {
			return out[i].Authorized
		}
		return strings.Compare(out[i].HostID, out[j].HostID) < 0
	})
	return out
}

func appendAddress(list []netip.AddrPort, add netip.AddrPort) []netip.AddrPort {
	if !add.Addr().IsValid() || add.Port() == 0 || len(list) >= 16 {
		return list
	}
	for _, existing := range list {
		if existing == add {
			return list
		}
	}
	return append(list, add)
}

func closer(a, b Locality) Locality {
	if b.rank() < a.rank() {
		return b
	}
	return a
}

// AllowedPeers derives the exact-fingerprint allowlist for pool.PeerOptions.
//
// It reads Authorized and nothing else. There is no branch here that consults
// Source, Locality, SeenOnLAN or any field a multicast packet can influence,
// and there must never be one: that is the single line of code where a hostile
// LAN would become a membership decision.
func AllowedPeers(peers []Peer) []string {
	out := make([]string, 0, len(peers))
	seen := map[string]bool{}
	for _, p := range peers {
		if !p.Authorized || !ValidFingerprint(p.Fingerprint) || seen[p.Fingerprint] {
			continue
		}
		seen[p.Fingerprint] = true
		out = append(out, p.Fingerprint)
	}
	sort.Strings(out)
	return out
}

// Unauthorized returns the peers that were found on the local link but are not
// in the coordinator's set. They exist so the UI can say "found, not authorized"
// instead of pretending an empty list, which is the honest thing to show on a
// shared office network — and a useful signal if somebody is advertising a
// fingerprint at you.
func Unauthorized(peers []Peer) []Peer {
	var out []Peer
	for _, p := range peers {
		if !p.Authorized {
			out = append(out, p)
		}
	}
	return out
}

// ICE (pion/ice, MIT, no CGo) plugs in exactly here and nowhere else: a future
// NAT-traversal pass would add gathered ICE candidates as additional entries in
// Peer.Addresses, with the coordinator relaying the candidate exchange as a small
// control flow. Two properties must survive that change. Authorization still
// comes from the coordinator's list — an ICE candidate is one more unauthenticated
// address claim, exactly like an mDNS TXT record. And bulk pager or file bytes
// must not be relayed through a Cloudflare service (§37.1): §16 makes that an
// account-action risk rather than an overage line, so a relayed transfer must use
// a donor-hosted relay with byte ceilings, or fail. No code path in this package
// relays bytes at all.
