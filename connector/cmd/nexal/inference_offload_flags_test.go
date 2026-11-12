package main

import (
	"strings"
	"testing"
)

func TestValidateOffloadFlags(t *testing.T) {
	cases := []struct {
		name         string
		mode         string
		prompt, wait int
		want         string // "" means accepted
	}{
		{"off", "off", 0, 0, ""},
		{"always", "always", 0, 0, ""},
		{"large with prompt", "large", 100, 0, ""},
		{"large with wait", "large", 0, 30, ""},
		{"large without limits", "large", 0, 0, "needs --max-prompt or --max-wait"},
		{"empty mode", "", 0, 0, "--mode must be"},
		{"unknown mode", "sometimes", 0, 0, "--mode must be"},
		{"prompt too big", "always", 10_000_001, 0, "--max-prompt must be"},
		{"negative prompt", "always", -1, 0, "--max-prompt must be"},
		{"wait too big", "always", 0, 3601, "--max-prompt must be"},
		{"negative wait", "always", 0, -1, "--max-prompt must be"},
		{"limits at bounds", "large", 10_000_000, 3600, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateOffloadFlags(c.mode, c.prompt, c.wait)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want error containing %q", err, c.want)
			}
		})
	}
}
