package mesh

import (
	"net"
	"strings"
	"sync"
	"time"
)

// Well-known ports the owner's panel offers quick links for. A service counts
// as offered when its port accepts a TCP connection over the tunnel; nothing
// is sent on the connection.
var servicePorts = []struct {
	Name string
	Port string
}{{"ssh", "22"}, {"vnc", "5900"}, {"smb", "445"}}

const (
	serviceProbeTimeout = 1500 * time.Millisecond // relayed and PQ-rekeying links answer well after 400 ms
	// A peer's offered services change rarely, and a reconnect (see
	// forgetServices) forces a fresh probe, so a stable link is re-probed slowly.
	serviceProbeTTL = 10 * time.Minute
)

type serviceProbe struct {
	at       time.Time
	services []string
}

var (
	serviceMu    sync.Mutex
	serviceCache = map[string]serviceProbe{}
	// dialService is replaced in tests.
	dialService = func(addr string) bool {
		c, err := net.DialTimeout("tcp", addr, serviceProbeTimeout)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}
)

// LocalServices is what THIS computer offers (Remote Login, Screen Sharing, File
// Sharing), checked on its own loopback address and cached like peer probes. The
// coordinator opens exactly these ports to the other computers in the network,
// which is why this must not depend on a peer probing us: on a deny-by-default
// network that probe is blocked until the port is opened.
func LocalServices() []string {
	return probeServices("127.0.0.1")
}

// probeServices returns the offered services on a peer's tunnel IP, cached for
// serviceProbeTTL so status polls do not dial the peer every time.
func probeServices(ip string) []string {
	serviceMu.Lock()
	if p, ok := serviceCache[ip]; ok && time.Since(p.at) < probeTTL(ip) {
		serviceMu.Unlock()
		return p.services
	}
	serviceMu.Unlock()

	found := make([]bool, len(servicePorts))
	var wg sync.WaitGroup
	for i, sp := range servicePorts {
		wg.Add(1)
		go func(i int, port string) {
			defer wg.Done()
			found[i] = dialService(net.JoinHostPort(ip, port))
		}(i, sp.Port)
	}
	wg.Wait()
	services := []string{}
	for i, ok := range found {
		if ok {
			services = append(services, servicePorts[i].Name)
		}
	}
	serviceMu.Lock()
	serviceCache[ip] = serviceProbe{at: time.Now(), services: services}
	serviceMu.Unlock()
	noteServiceProbe(ip, services)
	return services
}

// probeTTL: this computer's own services can be toggled in System Settings at
// any time, so the loopback probe stays fresh; peers get the long TTL.
func probeTTL(ip string) time.Duration {
	if ip == "127.0.0.1" {
		return time.Minute
	}
	return serviceProbeTTL
}

// forgetServices drops a peer's cached probe. Called when the peer is not
// connected, so the first poll after a reconnect (or tunnel address change,
// which is a different cache key) probes afresh.
func forgetServices(ip string) {
	if ip == "" {
		return
	}
	serviceMu.Lock()
	delete(serviceCache, ip)
	serviceMu.Unlock()
}

// isMobilePeer reports whether a peer is an iPhone or iPad, going by the names
// the apps register ("iphone", "iphone-28-93", "ipad-..."). Same rule as the Mac
// panel's isMobileDevice.
func isMobilePeer(names ...string) bool {
	for _, n := range names {
		n = strings.ToLower(n)
		if strings.Contains(n, "iphone") || strings.Contains(n, "ipad") {
			return true
		}
	}
	return false
}
