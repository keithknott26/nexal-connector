package agent

import (
	"net/netip"
	"testing"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustAddrPort(t *testing.T, s string) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

// The central claim of this surface: an address is offered only when it is genuinely
// dialable from this host.
func TestFirstReachableOnlyMatchesSharedNetworks(t *testing.T) {
	local := []netip.Prefix{
		mustPrefix(t, "192.168.1.0/24"),
		mustPrefix(t, "10.0.0.0/8"),
	}

	cases := []struct {
		name      string
		addresses []string
		wantAddr  string
		wantPort  uint16
		wantOK    bool
	}{
		{
			name:      "address on the same subnet is reachable",
			addresses: []string{"192.168.1.42:8443"},
			wantAddr:  "192.168.1.42", wantPort: 8443, wantOK: true,
		},
		{
			name: "a different private subnet is NOT reachable",
			// The mistake this guards against: 192.168.x is private, so it LOOKS
			// local, but a host on 192.168.1.0/24 cannot reach 192.168.9.0/24.
			addresses: []string{"192.168.9.5:8443"},
			wantOK:    false,
		},
		{
			name:      "a public address is not reachable merely for being routable",
			addresses: []string{"203.0.113.7:8443"},
			wantOK:    false,
		},
		{
			name:      "loopback is never a peer address",
			addresses: []string{"127.0.0.1:8443"},
			wantOK:    false,
		},
		{
			name:      "unspecified address is skipped",
			addresses: []string{"0.0.0.0:8443"},
			wantOK:    false,
		},
		{
			name: "peer preference order is respected among candidates",
			// Unreachable first: the peer's ordering must not cause an otherwise
			// reachable address to be missed.
			addresses: []string{"203.0.113.7:9000", "10.1.2.3:8443"},
			wantAddr:  "10.1.2.3", wantPort: 8443, wantOK: true,
		},
		{
			name:      "no addresses at all",
			addresses: nil,
			wantOK:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var addrs []netip.AddrPort
			for _, s := range tc.addresses {
				addrs = append(addrs, mustAddrPort(t, s))
			}
			addr, port, ok := firstReachable(addrs, local)
			if ok != tc.wantOK {
				t.Fatalf("reachable=%v, want %v (addr=%q)", ok, tc.wantOK, addr)
			}
			if !ok {
				if addr != "" || port != 0 {
					t.Errorf("unreachable peer still produced %s:%d -- a UI would show it", addr, port)
				}
				return
			}
			if addr != tc.wantAddr || port != tc.wantPort {
				t.Errorf("got %s:%d, want %s:%d", addr, port, tc.wantAddr, tc.wantPort)
			}
		})
	}
}

// An empty local set must not make everything reachable by vacuous truth.
func TestFirstReachableWithNoLocalNetworks(t *testing.T) {
	if _, _, ok := firstReachable([]netip.AddrPort{mustAddrPort(t, "192.168.1.5:8443")}, nil); ok {
		t.Error("a peer was called reachable with no local networks known")
	}
}

// localReachableSet must produce masked prefixes, since an unmasked prefix does not
// Contains() its own neighbours and every peer would look unreachable.
func TestLocalReachableSetReturnsMaskedPrefixes(t *testing.T) {
	for _, p := range localReachableSet() {
		if p != p.Masked() {
			t.Errorf("prefix %v is not masked; Contains would misbehave", p)
		}
		if p.Addr().IsLoopback() {
			t.Errorf("loopback prefix %v must be excluded", p)
		}
	}
}

// IPv6 must work through the same path, not silently fail.
func TestFirstReachableHandlesIPv6(t *testing.T) {
	local := []netip.Prefix{mustPrefix(t, "fd00:1234::/64")}
	addr, port, ok := firstReachable([]netip.AddrPort{mustAddrPort(t, "[fd00:1234::9]:8443")}, local)
	if !ok {
		t.Fatal("an IPv6 address on a shared prefix was not recognised")
	}
	if addr != "fd00:1234::9" || port != 8443 {
		t.Errorf("got %s:%d", addr, port)
	}
	// A different /64 is a different network.
	if _, _, ok := firstReachable([]netip.AddrPort{mustAddrPort(t, "[fd00:9999::9]:8443")}, local); ok {
		t.Error("an address on a different IPv6 prefix was called reachable")
	}
}

// Remote access is reported as unavailable, and no row may carry an address for an
// off-network peer. This is the honesty guarantee the UI depends on.
func TestPeerRowNeverCarriesAnAddressWithoutARoute(t *testing.T) {
	rows := []PeerRow{
		{Reachability: "no-route", Note: "x"},
		{Reachability: "unknown", Note: "y"},
	}
	for _, r := range rows {
		if r.Address != "" || r.Port != 0 {
			t.Errorf("%s row carried %s:%d", r.Reachability, r.Address, r.Port)
		}
		if r.Note == "" {
			t.Errorf("%s row has no explanation for the missing address", r.Reachability)
		}
	}
}
