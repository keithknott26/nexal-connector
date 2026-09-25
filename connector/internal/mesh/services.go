package mesh

import (
	"net"
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
	serviceProbeTimeout = 400 * time.Millisecond
	serviceProbeTTL     = 60 * time.Second
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

// probeServices returns the offered services on a peer's tunnel IP, cached for
// a minute so status polls do not dial the peer every time.
func probeServices(ip string) []string {
	serviceMu.Lock()
	if p, ok := serviceCache[ip]; ok && time.Since(p.at) < serviceProbeTTL {
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
	return services
}
