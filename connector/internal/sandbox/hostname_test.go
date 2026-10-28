package sandbox

import (
	"strings"
	"testing"
)

func TestValidHostname(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"a", true},
		{"dev-box1", true},
		{"0abc", true},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
		{"", false},
		{"-abc", false},
		{"abc-", false},
		{"Abc", false},
		{"a.b", false},
		{"a_b", false},
		{"a b", false},
		{"abc\n", false},
	}
	for _, c := range cases {
		if got := ValidHostname(c.in); got != c.want {
			t.Errorf("ValidHostname(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
