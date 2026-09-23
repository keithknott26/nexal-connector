package mesh

import "testing"

func TestUnavailableProviderFailsClosed(t *testing.T) {
	s := (UnavailableProvider{}).Snapshot()
	if s.ProviderAvailable || s.Lifecycle != LifecycleUnavailable || s.PQ != PQUnsupported || s.Peers == nil {
		t.Fatalf("unexpected unavailable status: %#v", s)
	}
}
