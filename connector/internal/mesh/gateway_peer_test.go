package mesh

import (
	"testing"
	"time"
)

func TestIsGatewayPeer(t *testing.T) {
	for name, want := range map[string]bool{
		"gw-abc123": true,
		"GW-ABC123": true,
		"gw-":       true,
		"gateway":   false,
		"mac-gw-1":  false,
		"":          false,
	} {
		if got := IsGatewayPeer(name); got != want {
			t.Errorf("IsGatewayPeer(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFreshRFC3339UsesUTC(t *testing.T) {
	loc := time.FixedZone("X", 5*3600)
	in := time.Date(2026, 1, 2, 8, 4, 5, 123, loc)
	got := FreshRFC3339(in)
	if want := "2026-01-02T03:04:05.000000123Z"; got != want {
		t.Fatalf("FreshRFC3339 = %q, want %q", got, want)
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil || !parsed.Equal(in) {
		t.Fatalf("round trip failed: %v %v", parsed, err)
	}
}
