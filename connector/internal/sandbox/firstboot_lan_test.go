package sandbox

import (
	"strings"
	"testing"
)

// The LAN address is informational: only a private IPv4 survives, and a bad one
// must not discard an otherwise valid report.
func TestParseFirstBootLanIPFiltering(t *testing.T) {
	for _, tc := range []struct{ lan, want string }{
		{"192.168.1.20", "192.168.1.20"},
		{"10.0.0.5", "10.0.0.5"},
		{"169.254.3.4", ""}, // self-assigned
		{"8.8.8.8", ""},     // public
		{"fd00::1", ""},     // IPv6
		{"not-an-ip", ""},   // garbage
	} {
		fb, ok, failed, _ := ParseFirstBootLine(FirstBootPrefix + `{"meshIp":"100.64.1.7","lanIp":"` + tc.lan + `"}`)
		if !ok || failed {
			t.Fatalf("lanIp %q: report rejected (ok=%v failed=%v)", tc.lan, ok, failed)
		}
		if fb.LanIP != tc.want {
			t.Errorf("lanIp %q: got %q, want %q", tc.lan, fb.LanIP, tc.want)
		}
	}
}

// A failure reason from the untrusted guest is capped at 200 bytes and stays valid UTF-8.
func TestParseFirstBootFailureReasonTruncated(t *testing.T) {
	long := strings.Repeat("é", 150) // 300 bytes; byte 200 splits a rune
	_, ok, failed, reason := ParseFirstBootLine(FirstBootFailedPrefix + " " + long)
	if ok || !failed {
		t.Fatalf("ok=%v failed=%v", ok, failed)
	}
	if len(reason) > 200 || !strings.HasPrefix(long, reason) {
		t.Errorf("reason not a <=200 byte prefix: len=%d", len(reason))
	}
	if reason != strings.ToValidUTF8(reason, "") {
		t.Errorf("reason has invalid UTF-8")
	}
}
