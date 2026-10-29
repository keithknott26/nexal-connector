package agent

import (
	"net/netip"
	"testing"
	"time"
)

// Zero-valued intervals fall back to the package defaults; only a positive
// override replaces them.
func TestDiscoveryOptionsIntervals(t *testing.T) {
	o := &DiscoveryOptions{}
	if got := o.advertiseEvery(); got != advertiseInterval {
		t.Errorf("default advertiseEvery = %v, want %v", got, advertiseInterval)
	}
	if got := o.mergeEvery(); got != mergeInterval {
		t.Errorf("default mergeEvery = %v, want %v", got, mergeInterval)
	}
	o = &DiscoveryOptions{AdvertiseInterval: -time.Second, MergeInterval: -time.Second}
	if o.advertiseEvery() != advertiseInterval || o.mergeEvery() != mergeInterval {
		t.Error("negative intervals must fall back to defaults")
	}
	o = &DiscoveryOptions{AdvertiseInterval: time.Second, MergeInterval: 2 * time.Second}
	if o.advertiseEvery() != time.Second || o.mergeEvery() != 2*time.Second {
		t.Errorf("overrides ignored: %v %v", o.advertiseEvery(), o.mergeEvery())
	}
}

// The LocalAddresses hook, when supplied, is used instead of the host's
// interfaces.
func TestDiscoveryOptionsLocalAddressesHook(t *testing.T) {
	want := netip.MustParseAddr("192.0.2.7")
	o := &DiscoveryOptions{LocalAddresses: func() []netip.Addr { return []netip.Addr{want} }}
	got := o.localAddresses()
	if len(got) != 1 || got[0] != want {
		t.Errorf("localAddresses = %v, want [%v]", got, want)
	}
}

// Without a hook, localAddresses enumerates the real interfaces; whatever the
// host has, the result is valid, unmapped, never loopback and capped at 64.
func TestDiscoveryOptionsLocalAddressesFallsBackToInterfaces(t *testing.T) {
	got := (&DiscoveryOptions{}).localAddresses()
	if len(got) > 64 {
		t.Errorf("localAddresses returned %d addresses, want at most 64", len(got))
	}
	for _, addr := range got {
		if !addr.IsValid() || addr.IsLoopback() || addr.Is4In6() {
			t.Errorf("unexpected address %v from interface enumeration", addr)
		}
	}
}
