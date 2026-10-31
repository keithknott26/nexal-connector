package agent

import (
	"testing"

	"nexal/connector/internal/mesh"
)

func TestSelfTunnelAddressOnlyReturnsValidTunnelAddresses(t *testing.T) {
	a, _ := testAgent(t)
	cases := map[string]string{
		"100.64.1.2":   "100.64.1.2",
		"192.168.1.5":  "",
		"100.64.001.2": "",
		"::1":          "",
		"":             "",
	}
	for in, want := range cases {
		a.meshProvider = &snapshotProvider{status: mesh.Status{SelfTunnelAddress: in}}
		if got := a.selfTunnelAddress(); got != want {
			t.Errorf("selfTunnelAddress(%q) = %q, want %q", in, got, want)
		}
	}
	a.meshProvider = nil
	if got := a.selfTunnelAddress(); got != "" {
		t.Errorf("nil provider = %q, want empty", got)
	}
}
