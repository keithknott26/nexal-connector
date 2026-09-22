package pool

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// Two Macs joined by a Thunderbolt bridge or a straight Ethernet run have no
// DHCP server between them: each end self-assigns a 169.254.0.0/16 (or fe80::/10)
// address and has nothing else. That is the topology the ring collective exists
// to serve, so both halves of its address rule — the endpoint a rank dials and
// the interface a rank binds — must accept link-local unicast.
//
// WHY THIS TEST EXISTS RATHER THAN A FIX: commit 132584f fixed exactly this
// rejection in experiments/tcp-pager, where PrivateAddress allowed loopback,
// RFC1918 and ULA but not link-local. internal/pool does NOT have that defect —
// privateIP in peer.go already admits IsLinkLocalUnicast, and the collective
// transport routes both checks through it — and a real two-process collective was
// run over this sandbox's 169.254.0.21 interface to confirm it end to end. This
// test pins that behaviour so the pager's bug cannot be introduced here later by
// someone tightening the rule to "private or loopback".
//
// Link-local is not a loosening. Routers must not forward it, so it cannot leave
// the physical segment — a stronger confinement property than the RFC1918 range
// already accepted, which does route across a home network. Being able to reach
// an address still authorizes nothing: every frame is carried over TLS 1.3 with
// the peer's exact device fingerprint pinned and its registry membership
// re-checked.
func TestRingAddressRuleAcceptsDirectlyCabledPeers(t *testing.T) {
	for _, endpoint := range []string{
		"https://169.254.0.21:48441", // IPv4 link-local: the cabled two-Mac case.
		"https://[fe80::1]:48441",    // IPv6 link-local.
		"https://127.0.0.1:48441",    // Loopback, single-host runs.
		"https://192.168.1.10:8443",  // RFC1918 LAN.
		"https://10.20.0.5:8443",     // RFC1918 across a VLAN.
		"https://[fd00::1]:8443",     // ULA.
	} {
		if _, err := validRingEndpoint(endpoint); err != nil {
			t.Errorf("validRingEndpoint(%q) = %v, want accepted", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"https://0.0.0.0:48441",           // Wildcard.
		"https://[::]:48441",              // IPv6 wildcard.
		"https://8.8.8.8:48441",           // Public route.
		"https://224.0.0.1:48441",         // Multicast.
		"https://[ff02::1]:48441",         // Link-local MULTICAST is not a unicast peer.
		"https://mac-studio.local:8443",   // A name: this transport performs no DNS lookup.
		"http://169.254.0.21:48441",       // Cleartext.
		"https://169.254.0.21",            // No explicit port.
		"https://169.254.0.21:48441/path", // A path: the route is fixed.
		"https://a:b@169.254.0.21:48441",  // Credentials in the URL.
	} {
		if _, err := validRingEndpoint(endpoint); err == nil {
			t.Errorf("validRingEndpoint(%q) = nil, want rejected", endpoint)
		}
	}
	// The listener side uses the same predicate, so a rank can bind the
	// self-assigned address its peer will dial.
	for _, ip := range []string{"169.254.0.21", "fe80::1", "127.0.0.1", "192.168.1.10", "fd00::1"} {
		if !privateIP(net.ParseIP(ip)) {
			t.Errorf("privateIP(%q) = false, want true", ip)
		}
	}
	for _, ip := range []string{"0.0.0.0", "::", "8.8.8.8", "224.0.0.1", "ff02::1"} {
		if privateIP(net.ParseIP(ip)) {
			t.Errorf("privateIP(%q) = true, want false", ip)
		}
	}
}

// And the rule has to hold for a real ring, not only for the predicate: a rank
// must be able to serve on a link-local interface and another rank must be able
// to complete an all-reduce against it. Skipped where no link-local address is
// configured, because inventing one would test nothing.
func TestRingAllReduceOverLinkLocalInterface(t *testing.T) {
	local := linkLocalIPv4(t)
	if local == "" {
		t.Skip("no IPv4 link-local address is configured on this machine")
	}
	registry := NewRegistry(nil)
	identities := [2]Identity{enrolledIdentity(t, registry), enrolledIdentity(t, registry)}
	var fprints [2]string
	var listeners [2]net.Listener
	var endpoints [2]string
	for i := range identities {
		fprints[i] = DeviceID(identities[i].PublicKey)
		listener, err := net.Listen("tcp", net.JoinHostPort(local, "0"))
		if err != nil {
			t.Skipf("cannot bind the link-local address %s: %v", local, err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		listeners[i] = listener
		endpoints[i] = "https://" + listener.Addr().String()
	}
	rings := make([]*Ring, 2)
	for i := range identities {
		other := 1 - i
		ring, err := NewRing(RingOptions{Identity: identities[i], Registry: registry, Tenant: testTenant,
			AuthorizedPeers: []string{fprints[other]},
			Members: []RingMember{
				{Fingerprint: fprints[i], Tenant: testTenant},
				{Fingerprint: fprints[other], Tenant: testTenant, Endpoint: endpoints[other]},
			},
			StepTimeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("rank %d: %v", i, err)
		}
		rings[i] = ring
		go func() { _ = ring.Serve(listeners[i]) }()
		for waited := 0; !ring.transport.serving(); waited++ {
			if waited > 2000 {
				t.Fatal("ring server did not start on the link-local address")
			}
			time.Sleep(time.Millisecond)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := ring.Shutdown(ctx); err != nil {
				_ = ring.Close()
			}
		})
	}
	// Rank order is the fingerprint ordering, so index the vectors by the ring's
	// own rank rather than by construction order.
	vectors := make([][]float64, 2)
	for i, ring := range rings {
		if ring.Rank() == 0 {
			vectors[i] = []float64{1, 2, 3, 4}
			continue
		}
		vectors[i] = []float64{10, 20, 30, 40}
	}
	session := strings.Repeat("7", 64)
	errs := runAll(t, session, ReduceSum, 20*time.Second, rings, vectors)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("rank %d all-reduce over link-local: %v", i, err)
		}
	}
	want := []float64{11, 22, 33, 44}
	for i, got := range vectors {
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("rank %d result = %v, want %v", i, got, want)
			}
		}
	}
}

// linkLocalIPv4 returns a configured 169.254.0.0/16 address, or "" if the machine
// has none. It reads the real interface list rather than assuming an address.
func linkLocalIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		network, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := network.IP.To4(); ip != nil && ip.IsLinkLocalUnicast() {
			return ip.String()
		}
	}
	return ""
}
