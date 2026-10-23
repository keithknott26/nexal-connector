package agent

import "testing"

// localInterfaceAddresses must never publish loopback addresses, mapped IPv4
// forms, or more than the 64-address cap, whatever the host's interfaces are.
func TestLocalInterfaceAddressesInvariants(t *testing.T) {
	got := localInterfaceAddresses()
	if len(got) > 64 {
		t.Fatalf("got %d addresses, want at most 64", len(got))
	}
	for _, a := range got {
		if !a.IsValid() {
			t.Errorf("invalid address in result: %v", a)
		}
		if a.IsLoopback() {
			t.Errorf("loopback address published: %v", a)
		}
		if a.Is4In6() {
			t.Errorf("address not unmapped: %v", a)
		}
	}
}
