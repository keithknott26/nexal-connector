package discovery

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestIsNexalTarget(t *testing.T) {
	long := strings.Repeat("a", 240) + ".mesh.nexal.systems" // over the DNS limit
	cases := []struct {
		target string
		want   bool
	}{
		{"studio.mesh.nexal.systems", true},
		{"Studio.MESH.Nexal.Systems", true},
		{".mesh.nexal.systems", false}, // suffix only, no host label
		{"studio.example.com", false},
		{"studio.mesh.nexal.systems.evil.com", false},
		{"stu dio.mesh.nexal.systems", false},
		{"a/b.mesh.nexal.systems", false},
		{"a\\b.mesh.nexal.systems", false},
		{"user@host.mesh.nexal.systems", false},
		{"host\n.mesh.nexal.systems", false},
		{long, false},
		{"", false},
	}
	for _, c := range cases {
		if got := isNexalTarget(c.target); got != c.want {
			t.Errorf("isNexalTarget(%q) = %v, want %v", c.target, got, c.want)
		}
	}
}

func TestBridgeRecordExpiresAtAddsTTLSeconds(t *testing.T) {
	observed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got := BridgeRecord{TTL: 90}.ExpiresAt(observed)
	if want := observed.Add(90 * time.Second); !got.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", got, want)
	}
}

func TestValidTXTValueBoundsLengthAndCharset(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain", "abc-123", true},
		{"space and tilde are printable", "a ~", true},
		{"empty", "", false},
		{"64 bytes ok", strings.Repeat("x", 64), true},
		{"65 bytes rejected", strings.Repeat("x", 65), false},
		{"newline", "a\nb", false},
		{"escape", "a\x1bb", false},
		{"DEL", "a\x7f", false},
		{"non-ascii", "café", false},
	}
	for _, c := range cases {
		if got := validTXTValue(c.in); got != c.want {
			t.Errorf("%s: validTXTValue(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestLocalAddressAcceptsOnlyDialableLocalRanges(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"10.1.2.3", true},
		{"192.168.0.5", true},
		{"172.16.0.1", true},
		{"127.0.0.1", true},
		{"169.254.1.1", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"::ffff:192.168.1.1", true}, // v4-mapped is unmapped first
		{"fd00::1", true},
		{"fe80::1", true},
		{"100.128.0.1", false}, // just outside CGNAT
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
		{"0.0.0.0", false},
		{"224.0.0.251", false},
		{"ff02::fb", false},
	}
	for _, c := range cases {
		if got := localAddress(netip.MustParseAddr(c.addr)); got != c.want {
			t.Errorf("localAddress(%s) = %v, want %v", c.addr, got, c.want)
		}
	}
	if localAddress(netip.Addr{}) {
		t.Error("zero Addr must not be local")
	}
}
