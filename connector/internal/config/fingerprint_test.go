package config

import (
	"strings"
	"testing"
)

func TestValidDeviceFingerprint(t *testing.T) {
	hex64 := strings.Repeat("0123456789abcdef", 4)
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"lowercase hex", hex64, true},
		{"empty", "", false},
		{"too short", hex64[:63], false},
		{"too long", hex64 + "0", false},
		{"uppercase rejected", strings.ToUpper(hex64), false},
		{"mixed case rejected", "A" + hex64[1:], false},
		{"char just past f", "g" + hex64[1:], false},
		{"char just before 0", "/" + hex64[1:], false},
		{"char just past 9", ":" + hex64[1:], false},
		{"trailing space", hex64[:63] + " ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validDeviceFingerprint(c.in); got != c.want {
				t.Fatalf("validDeviceFingerprint(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
