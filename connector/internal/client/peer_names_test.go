package client

import (
	"strings"
	"testing"
)

func TestValidPeerAddress(t *testing.T) {
	for in, want := range map[string]bool{
		"100.64.0.1":        true,
		"100.127.255.254":   true,
		"100.63.255.255":    false,
		"100.128.0.1":       false,
		"10.0.0.1":          false,
		"::ffff:100.64.0.1": false,
		"fd7a::1":           false,
		"":                  false,
		"not-an-ip":         false,
	} {
		if got := ValidPeerAddress(in); got != want {
			t.Errorf("ValidPeerAddress(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCleanPeerName(t *testing.T) {
	if got, err := CleanPeerName("  Studio Mac \n"); err != nil || got != "Studio Mac" {
		t.Errorf("trim: got %q, %v", got, err)
	}
	if got, err := CleanPeerName("   "); err != nil || got != "" {
		t.Errorf("blank removes the name: got %q, %v", got, err)
	}
	if _, err := CleanPeerName(strings.Repeat("é", MaxPeerName)); err != nil {
		t.Errorf("limit counts characters, not bytes: %v", err)
	}
	if _, err := CleanPeerName(strings.Repeat("a", MaxPeerName+1)); err == nil {
		t.Error("expected too-long error")
	}
	if _, err := CleanPeerName("bad\x00name"); err == nil {
		t.Error("expected control character error")
	}
}

func TestSetPeerNameRejectsBadInputBeforeNetwork(t *testing.T) {
	c := &Client{}
	if _, err := c.SetPeerName(nil, "host", "8.8.8.8", "x"); err == nil {
		t.Error("expected invalid address error")
	}
	if _, err := c.SetPeerName(nil, "host", "100.64.0.1", "bad\tname"); err == nil {
		t.Error("expected invalid name error")
	}
}
