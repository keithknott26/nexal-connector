package agent

import (
	"net"
	"net/netip"
	"sort"
	"strings"

	"nexal/connector/internal/mesh"
)

// PeerRow is one row of the "other Macs on the network" surface.
//
// Reachability is decided HERE rather than in the UI. The Mac app asked for the address
// to ssh or VNC to, and the only honest answer depends on facts the UI does not hold:
// which addresses were advertised, whether any is on a network this host shares, and
// what ObservedWANAddress actually means. A UI left to guess would reach for the WAN
// address, which is wrong in a way that looks right (see Reachability below).
type PeerRow struct {
	HostID      string `json:"hostId"`
	Name        string `json:"name,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Authorized  bool   `json:"authorized"`
	Configured  bool   `json:"configured"`
	SeenOnLAN   bool   `json:"seenOnLan"`

	// Reachability is one of:
	//
	//   "same-network"  an advertised address is directly dialable from this host, so
	//                   Address/Port are set and ssh/VNC to them is real.
	//   "no-route"      the peer is known and authorized, but nothing it advertised is
	//                   reachable from here. Address is EMPTY on purpose.
	//   "unknown"       nothing has been advertised yet.
	//
	// There is deliberately no case that yields an address for an off-network peer.
	// PeerView.ObservedWANAddress is what the coordinator saw an advertisement ARRIVE
	// FROM -- the peer's NAT router, not the peer -- so presenting it as an ssh target
	// would print an address that cannot work and looks like it should. A public
	// hostname is no better: cloudflared ingress is HTTP(S) to a local port, not a host
	// an ssh client can reach.
	Reachability string `json:"reachability"`
	Address      string `json:"address,omitempty"`
	Port         uint16 `json:"port,omitempty"`

	// Note explains a missing address in the surface's own words, so the UI never has
	// to invent a reason for an empty field.
	Note string `json:"note,omitempty"`
}

// PeersResponse is GET /v1/peers.
type PeersResponse struct {
	Peers []PeerRow `json:"peers"`
	// SelfAddresses are this host's own advertisable addresses, so the panel can show
	// what other Macs would use to reach it.
	SelfAddresses []string `json:"selfAddresses,omitempty"`
	// DirectoryStale reports that the coordinator list is the last known one rather
	// than a fresh read, rather than silently showing an old list as current.
	DirectoryStale bool `json:"directoryStale"`
	// LastError is already scrubbed by the client package.
	LastError string `json:"lastError,omitempty"`
	// RemoteAccessAvailable is false while no private route exists between Macs on
	// different networks. It is reported so the UI can state the limitation instead of
	// implying that an empty list means "no other Macs".
	RemoteAccessAvailable bool `json:"remoteAccessAvailable"`
}

// PeersSnapshot builds the peer surface from the agent's merged view.
func (a *Agent) PeersSnapshot() PeersResponse {
	view := a.PeerCandidates()
	local := localReachableSet()

	out := PeersResponse{
		Peers:          make([]PeerRow, 0, len(view.Peers)),
		DirectoryStale: view.Directory.Stale,
		LastError:      view.LastAdvertiseError,
		// Hardcoded false: no private network route between Macs exists in this build.
		// It is a field rather than an omission so the UI states the limit explicitly.
		RemoteAccessAvailable: false,
	}

	for _, p := range view.Peers {
		row := PeerRow{
			HostID:      p.HostID,
			Name:        p.Name,
			Fingerprint: p.Fingerprint,
			Authorized:  p.Authorized,
			Configured:  p.Configured,
			SeenOnLAN:   p.SeenOnLAN,
		}
		if addr, port, ok := firstReachable(p.Addresses, local); ok {
			row.Reachability = "same-network"
			row.Address = addr
			row.Port = port
		} else if len(p.Addresses) == 0 {
			row.Reachability = "unknown"
			row.Note = "No addresses advertised yet."
		} else {
			row.Reachability = "no-route"
			row.Note = "Linked, but not on a network this Mac can reach. " +
				"ssh and VNC need a private route between the two Macs, which this build does not set up."
		}
		out.Peers = append(out.Peers, row)
	}

	// Macs on the secure network. These come from the tunnel runtime, not LAN
	// discovery, so they appear wherever the two Macs are.
	a.mu.Lock()
	provider := a.meshProvider
	a.mu.Unlock()
	if _, none := provider.(mesh.UnavailableProvider); !none && provider != nil {
		status := mesh.SanitizeSnapshot(provider.Snapshot())
		if status.ProviderAvailable {
			out.RemoteAccessAvailable = true
			for _, p := range status.Peers {
				out.Peers = append(out.Peers, meshPeerRow(p))
			}
		}
	}

	// Reachable peers first, then by name, so the list a user can act on is at the top
	// and the order does not churn between polls.
	sort.SliceStable(out.Peers, func(i, j int) bool {
		li := out.Peers[i].Reachability == "same-network"
		lj := out.Peers[j].Reachability == "same-network"
		if li != lj {
			return li
		}
		return out.Peers[i].displayName() < out.Peers[j].displayName()
	})

	// Through the DiscoveryOptions hook when one is configured, so tests that inject
	// addresses see their injected set here too rather than the real interfaces.
	if a.discovery != nil {
		for _, la := range advertisableAddresses(a.discovery.localAddresses(), 0) {
			out.SelfAddresses = append(out.SelfAddresses, la.Address)
		}
	}
	return out
}

func meshPeerRow(p mesh.Peer) PeerRow {
	row := PeerRow{HostID: "mesh:" + p.ID, Name: p.Name, Fingerprint: p.ID, Authorized: true, Configured: true}
	up := p.Lifecycle == mesh.LifecycleConnected || p.Lifecycle == mesh.LifecycleDegraded
	if up && p.TunnelAddress != "" {
		row.Reachability, row.Address = "same-network", p.TunnelAddress
		path := p.PathLabel
		if path == "" {
			path = "Secure network"
		}
		row.Note = strings.TrimSpace("Connected · " + path)
		return row
	}
	row.Reachability = "unknown"
	row.Note = "On your secure network; not connected right now."
	return row
}

func (r PeerRow) displayName() string {
	if r.Name != "" {
		return r.Name
	}
	return r.HostID
}

// firstReachable picks the first advertised address that shares a network with this
// host. Preference order from the peer is respected among equally reachable candidates.
func firstReachable(addrs []netip.AddrPort, local []netip.Prefix) (string, uint16, bool) {
	for _, ap := range addrs {
		if !ap.IsValid() {
			continue
		}
		ip := ap.Addr()
		// Loopback belongs to whoever is asking, never to a peer.
		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		for _, p := range local {
			if p.Contains(ip) {
				return ip.String(), ap.Port(), true
			}
		}
	}
	return "", 0, false
}

// localReachableSet returns the networks this host is directly attached to, as prefixes
// rather than bare addresses.
//
// Prefixes are required, not a convenience: deciding "same network" by comparing bare
// addresses means either an exact match (never true for a peer) or a guess at the mask.
// The interface's own mask is the only correct source, so it is read here instead of
// assuming /24.
//
// Point-to-point and loopback interfaces are skipped for the same reason
// localInterfaceAddresses skips them: a utun VPN takes that shape, and treating a VPN
// prefix as "my network" would advertise an ssh target across a tunnel that may not
// carry it -- exactly the false-confidence this endpoint exists to avoid.
func localReachableSet() []netip.Prefix {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
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
			ip, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			ones, _ := ipnet.Mask.Size()
			if ones == 0 {
				continue
			}
			p, err := ip.Prefix(ones)
			if err != nil {
				continue
			}
			out = append(out, p.Masked())
		}
	}
	return out
}
