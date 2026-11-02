package config

import (
	"strings"
	"testing"
)

func TestValidResourceIDAcceptsOnlyShortAlnumDashUnderscore(t *testing.T) {
	good := []string{"a", "Z9", "ep-1_x", strings.Repeat("a", 64)}
	for _, s := range good {
		if !validResourceID(s) {
			t.Errorf("validResourceID(%q) = false, want true", s)
		}
	}
	bad := []string{"", strings.Repeat("a", 65), "a b", "a/b", "a.b", "a\n", "é"}
	for _, s := range bad {
		if validResourceID(s) {
			t.Errorf("validResourceID(%q) = true, want false", s)
		}
	}
}

func TestValidOverflowPeersAllowsIPsAndWildcardOnly(t *testing.T) {
	if err := validOverflowPeers("x", []string{"*", "10.0.0.1", "fe80::1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validOverflowPeers("x", nil); err != nil {
		t.Fatalf("nil peers: %v", err)
	}
	err := validOverflowPeers("overflow gpu", []string{"10.0.0.1", "host.example"})
	if err == nil || !strings.Contains(err.Error(), "overflow gpu: host.example") {
		t.Fatalf("want error naming the bad peer, got %v", err)
	}
}
