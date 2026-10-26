package client

import (
	"strings"
	"testing"
)

func TestWireTimestampNormalizesToUTCMillis(t *testing.T) {
	cases := map[string]string{
		"2026-01-02T03:04:05Z":           "2026-01-02T03:04:05.000Z",
		"2026-01-02T03:04:05.123456789Z": "2026-01-02T03:04:05.123Z",
		"2026-01-02T05:04:05.5+02:00":    "2026-01-02T03:04:05.500Z",
		"":                               "",
		"yesterday":                      "",
		"2026-01-02 03:04:05":            "",
	}
	for in, want := range cases {
		if got := wireTimestamp(in); got != want {
			t.Errorf("wireTimestamp(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanMessageStripsControlsAndBoundsLength(t *testing.T) {
	if got := cleanMessage("  hello\tworld\n\x00\x1b[0m  "); got != "hello world [0m" {
		t.Errorf("cleanMessage control handling = %q", got)
	}
	if got := cleanMessage(strings.Repeat("a", 1000)); len(got) != 240 {
		t.Errorf("cleanMessage length = %d, want 240", len(got))
	}
	if got := cleanMessage(""); got != "" {
		t.Errorf("cleanMessage(empty) = %q", got)
	}
}

func TestContainsGuestStatusAcceptsOnlyLiveStates(t *testing.T) {
	for _, s := range []string{"provisioning", "authorizing", "joining", "paired"} {
		if !containsGuestStatus(s) {
			t.Errorf("%q should be a live guest status", s)
		}
	}
	for _, s := range []string{"", "Paired", "revoked", "expired"} {
		if containsGuestStatus(s) {
			t.Errorf("%q should not be a live guest status", s)
		}
	}
}
