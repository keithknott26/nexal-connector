package remoteinference

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// LAN ring. MLX's ring traffic between pooled Macs normally crosses the neXal mesh. Macs that share a private
// LAN send it over the LAN instead: much faster than the mesh, and not subject to its key renewals. That traffic
// is then protected only by the LAN (as the owner chose), so each listener accepts only the pool's own Macs.
//
// The coordinator's ring stays the members' neXal addresses (ring changes are detected on it); each member also
// publishes its LAN ring address. When this Mac starts its share of the pool it builds its own host list for MLX:
//   - its own entry on loopback, with relays on its neXal and LAN addresses forwarding to it (a Mac cannot
//     connect to its own neXal address, and MLX binds exactly the address it is given);
//   - every other member on its LAN address when that address is on one of this Mac's LANs, else its neXal one.

// lanNets is this Mac's private LAN networks. Runner.LAN (tests) is taken as /24 networks.
func (r *Runner) lanNets() []*net.IPNet {
	if r.NoLAN {
		return nil
	}
	if r.LAN != nil {
		var out []*net.IPNet
		for _, a := range r.LAN() {
			if ip := net.ParseIP(a).To4(); ip != nil {
				out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(24, 32)})
			}
		}
		return out
	}
	return lanIPNets()
}

// ringLAN is this Mac's LAN ring address for port ("" when it has no LAN or the port is taken there).
func (r *Runner) ringLAN(addr string, port int) string {
	if ip := net.ParseIP(addr); ip == nil || ip.IsLoopback() {
		return "" // tests: the "neXal" address is loopback, there is no LAN to prefer
	}
	for _, n := range r.lanNets() {
		a := net.JoinHostPort(n.IP.String(), strconv.Itoa(port))
		if l, err := net.Listen("tcp", a); err == nil {
			l.Close()
			return a
		}
	}
	return ""
}

// ringPlan is what this Mac's MLX gets for one pooled run, and the relays that run with it.
type ringPlan struct {
	hosts  []string
	relays []io.Closer
}

func (p *ringPlan) close() {
	for _, c := range p.relays {
		_ = c.Close()
	}
}

// planRing builds this Mac's host list (see above). ring and lanRing come from the coordinator, in rank order;
// addr is this Mac's neXal address and port its ring port.
func (r *Runner) planRing(ctx context.Context, ring, lanRing []string, rank, port int, addr string) (*ringPlan, error) {
	plan := &ringPlan{hosts: append([]string(nil), ring...)}
	if ip := net.ParseIP(addr); ip == nil || ip.IsLoopback() || rank >= len(ring) {
		return plan, nil
	}
	var meshPeers, lanPeers map[string]bool
	plan.hosts, meshPeers, lanPeers = planHosts(ring, lanRing, rank, port, r.lanNets())
	local := plan.hosts[rank]
	mesh, err := ringRelay(ctx, net.JoinHostPort(addr, strconv.Itoa(port)), local, meshPeers)
	if err != nil {
		return nil, err
	}
	plan.relays = append(plan.relays, mesh)
	if rank < len(lanRing) && lanRing[rank] != "" && len(lanPeers) > 0 {
		if lan, err := ringRelay(ctx, lanRing[rank], local, lanPeers); err == nil {
			plan.relays = append(plan.relays, lan)
		} else {
			r.Logger.Warn("pool ring: cannot listen on the LAN; the other Macs fall back to the neXal address", "error", err.Error())
		}
	}
	return plan, nil
}

// planHosts is this Mac's MLX host list: its own entry on loopback, each other member on its LAN ring address
// when that is on one of nets, else its neXal one. It also returns the addresses each relay accepts.
func planHosts(ring, lanRing []string, rank, port int, nets []*net.IPNet) (hosts []string, meshPeers, lanPeers map[string]bool) {
	hosts = append([]string(nil), ring...)
	meshPeers, lanPeers = map[string]bool{}, map[string]bool{}
	onMyLAN := func(a string) bool {
		h, _, err := net.SplitHostPort(a)
		ip := net.ParseIP(h)
		if err != nil || ip == nil {
			return false
		}
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	for i := range ring {
		if i == rank {
			continue
		}
		if h, _, err := net.SplitHostPort(ring[i]); err == nil {
			meshPeers[h] = true
		}
		if i < len(lanRing) && lanRing[i] != "" && onMyLAN(lanRing[i]) {
			hosts[i] = lanRing[i]
			h, _, _ := net.SplitHostPort(lanRing[i])
			lanPeers[h] = true
		}
	}
	hosts[rank] = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	return hosts, meshPeers, lanPeers
}

// ringRelay listens on listen and pipes each connection from an allowed address to target.
func ringRelay(ctx context.Context, listen, target string, allow map[string]bool) (io.Closer, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			h, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			if !allow[h] {
				_ = c.Close()
				continue
			}
			go pipeTo(ctx, c, target)
		}
	}()
	return ln, nil
}

func pipeTo(ctx context.Context, c net.Conn, target string) {
	d, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", target)
	if err != nil {
		_ = c.Close()
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	half := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}
	go half(d, c)
	go half(c, d)
	wg.Wait()
	_ = c.Close()
	_ = d.Close()
}

// lanIPNets is every private IPv4 network of an up interface that is not loopback, the neXal tunnel or a VM bridge.
func lanIPNets() []*net.IPNet {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, in := range ifs {
		if !lanInterface(in) {
			continue
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
					out = append(out, &net.IPNet{IP: ip4, Mask: ipn.Mask})
				}
			}
		}
	}
	return out
}
