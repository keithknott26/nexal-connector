package client

import (
	"net/netip"
	"strings"
	"testing"
)

func TestValidIDBoundsAndAlphabet(t *testing.T) {
	cases := map[string]bool{
		"":                       false,
		"a":                      true,
		"Host_01-x":              true,
		strings.Repeat("a", 128): true,
		strings.Repeat("a", 129): false,
		"has space":              false,
		"dot.ed":                 false,
		"slash/y":                false,
		"new\nline":              false,
		"ünï":                    false,
	}
	for id, want := range cases {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestCleanCodeAcceptsOnlyLowerSnake(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"rate_limited":          "rate_limited",
		"E404":                  "",
		"bad-code":              "",
		"has space":             "",
		strings.Repeat("a", 64): strings.Repeat("a", 64),
		strings.Repeat("a", 65): "",
	}
	for in, want := range cases {
		if got := cleanCode(in); got != want {
			t.Errorf("cleanCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCGNATAddressCoversExactly100_64_10(t *testing.T) {
	cases := map[string]bool{
		"100.63.255.255":  false,
		"100.64.0.0":      true,
		"100.127.255.255": true,
		"100.128.0.0":     false,
		"10.0.0.1":        false,
		"fd00::1":         false,
	}
	for s, want := range cases {
		if got := cgnatAddress(netip.MustParseAddr(s)); got != want {
			t.Errorf("cgnatAddress(%s) = %v, want %v", s, got, want)
		}
	}
}
