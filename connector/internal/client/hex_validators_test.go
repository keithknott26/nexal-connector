package client

import "testing"

func TestHexValidatorsAcceptOnly64LowercaseHex(t *testing.T) {
	good := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := map[string]bool{
		good:            true,
		"":              false,
		good[:63]:       false,
		good + "0":      false,
		"A" + good[1:]:  false, // uppercase rejected
		"g" + good[1:]:  false, // non-hex letter
		" " + good[1:]:  false,
		good[:63] + "é": false, // multibyte rune
	}
	for in, want := range cases {
		if got := validDigest(in); got != want {
			t.Errorf("validDigest(%q) = %v, want %v", in, got, want)
		}
		if got := validFingerprint(in); got != want {
			t.Errorf("validFingerprint(%q) = %v, want %v", in, got, want)
		}
	}
}
