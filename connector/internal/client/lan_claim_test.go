package client

import "testing"

func TestValidLANAddressClaim(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"192.168.1.5", "192.168.1.5", true},
		{"10.0.0.1", "10.0.0.1", true},
		{"172.16.0.9", "172.16.0.9", true},
		{"100.64.0.1", "100.64.0.1", true},
		{"169.254.1.1", "169.254.1.1", true},
		{"fe80::1", "fe80::1", true},
		{"8.8.8.8", "", false},
		{"100.128.0.1", "", false},
		{"010.0.0.1", "", false},
		{"fe80::1%en0", "", false},
		{"FE80::1", "", false},
		{"::ffff:192.168.1.5", "", false},
		{"2001:db8::1", "", false},
		{"", "", false},
		{"not-an-ip", "", false},
	}
	for _, c := range cases {
		got, ok := ValidLANAddressClaim(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ValidLANAddressClaim(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
