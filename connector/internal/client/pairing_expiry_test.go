package client

import (
	"testing"
	"time"
)

func TestParsePairingExpiry(t *testing.T) {
	want := time.Date(2026, 10, 21, 12, 30, 45, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value string
		want  time.Time
	}{
		{"bare seconds", "2026-10-21T12:30:45Z", want},
		{"milliseconds", "2026-10-21T12:30:45.123Z", want.Add(123 * time.Millisecond)},
		{"nanoseconds", "2026-10-21T12:30:45.123456789Z", want.Add(123456789 * time.Nanosecond)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePairingExpiry(tc.value)
			if err != nil {
				t.Fatalf("ParsePairingExpiry(%q): %v", tc.value, err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("ParsePairingExpiry(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestParsePairingExpiryRejects(t *testing.T) {
	for name, value := range map[string]string{
		"empty":           "",
		"offset not Z":    "2026-10-21T12:30:45+00:00",
		"lowercase z":     "2026-10-21T12:30:45z",
		"date only":       "2026-10-21",
		"space separator": "2026-10-21 12:30:45Z",
		"too many digits": "2026-10-21T12:30:45.1234567890Z",
		"month 13":        "2026-13-21T12:30:45Z",
		"trailing junk":   "2026-10-21T12:30:45Z ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePairingExpiry(value); err == nil {
				t.Fatalf("ParsePairingExpiry(%q) succeeded, want error", value)
			}
		})
	}
}
