package client

import (
	"strings"
	"testing"
)

func TestValidTunnelAddress(t *testing.T) {
	for s, want := range map[string]bool{
		"100.64.0.1":        true,
		"100.127.255.254":   true,
		"100.63.255.255":    false,
		"100.128.0.0":       false,
		"10.0.0.1":          false,
		"100.64.0.01":       false, // non-canonical
		" 100.64.0.1":       false,
		"::ffff:100.64.0.1": false,
		"":                  false,
	} {
		if got := ValidTunnelAddress(s); got != want {
			t.Errorf("ValidTunnelAddress(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestValidExitDeviceID(t *testing.T) {
	const ok = "0123abcd-0123-abcd-0123-0123456789ab"
	for s, want := range map[string]bool{
		ok:                                   true,
		strings.ToUpper(ok):                  false,
		ok + "0":                             false,
		"\n" + ok:                            false,
		"0123abcd01234abcd0123-0123456789ab": false,
		"":                                   false,
	} {
		if got := ValidExitDeviceID(s); got != want {
			t.Errorf("ValidExitDeviceID(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestValidGuestHostname(t *testing.T) {
	long := strings.Repeat("a", 60)
	for _, c := range []struct {
		grant, host string
		want        bool
	}{
		{"abc-123", "nexal-guest-abc-123", true},
		{"abc", "nexal-guest-abd", false},
		{"ABC", "nexal-guest-ABC", false},
		{"a_b", "nexal-guest-a_b", false},
		{long, "nexal-guest-" + long, false}, // over 63 chars
	} {
		if got := ValidGuestHostname(c.grant, c.host); got != c.want {
			t.Errorf("ValidGuestHostname(%q, %q) = %v, want %v", c.grant, c.host, got, c.want)
		}
	}
}
