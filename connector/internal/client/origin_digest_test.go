package client

import (
	"strings"
	"testing"
)

func TestValidHTTPSOrigin(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://example.com":      true,
		"https://example.com:8443": true,
		"http://example.com":       false,
		"https://":                 false,
		"https://example.com/":     false,
		"https://example.com/path": false,
		"https://example.com?q=1":  false,
		"https://example.com?":     false,
		"https://example.com#frag": false,
		"https://user@example.com": false,
		"https://exa mple.com":     false,
		"https://example.com\n":    false,
		"":                         false,
	} {
		if got := validHTTPSOrigin(raw); got != want {
			t.Errorf("validHTTPSOrigin(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestValidDigestAndFingerprint(t *testing.T) {
	good := strings.Repeat("0a9f", 16)
	for name, fn := range map[string]func(string) bool{"validDigest": validDigest, "validFingerprint": validFingerprint} {
		for v, want := range map[string]bool{
			good:                    true,
			good[:63]:               false,
			good + "0":              false,
			strings.ToUpper(good):   false,
			good[:63] + "g":         false,
			"":                      false,
			strings.Repeat(" ", 64): false,
		} {
			if got := fn(v); got != want {
				t.Errorf("%s(%q) = %v, want %v", name, v, got, want)
			}
		}
	}
}
