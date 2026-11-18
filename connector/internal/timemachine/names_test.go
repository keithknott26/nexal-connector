package timemachine

import (
	"net/netip"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	good := []string{"Backup", "my-share_1", "Time Machine", "a", strings.Repeat("x", 64)}
	for _, s := range good {
		if err := validateName(s); err != nil {
			t.Errorf("validateName(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{"", " lead", "trail ", "-dash", strings.Repeat("x", 65), "a/b", "a.b", "a;b", "naïve", "tab\tx", "a\nb"}
	for _, s := range bad {
		if err := validateName(s); err == nil {
			t.Errorf("validateName(%q) = nil, want error", s)
		}
	}
}

func TestMeshAddress(t *testing.T) {
	cases := map[string]bool{
		"10.1.2.3":        true,
		"172.16.0.1":      true,
		"192.168.1.1":     true,
		"100.64.0.1":      true,
		"100.127.255.254": true,
		"100.63.255.255":  false,
		"100.128.0.0":     false,
		"8.8.8.8":         false,
		"fd00::1":         true,
		"2001:db8::1":     false,
	}
	for s, want := range cases {
		if got := meshAddress(netip.MustParseAddr(s)); got != want {
			t.Errorf("meshAddress(%s) = %v, want %v", s, got, want)
		}
	}
}
