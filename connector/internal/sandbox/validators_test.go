package sandbox

import (
	"strings"
	"testing"
)

func TestValidSecretAndToken(t *testing.T) {
	cases := []struct {
		in            string
		secret, token bool
	}{
		{"abc.DEF_123~+=-", true, true},
		{"", false, false},
		{"has space", false, false},
		{"quote'", false, false},
		{"new\nline", false, false},
		{"a/b", false, true}, // '/' is allowed in tokens only
		{strings.Repeat("a", 256), true, true},
		{strings.Repeat("a", 257), false, true},
		{strings.Repeat("a", 2049), false, false},
	}
	for _, c := range cases {
		if got := validSecret(c.in); got != c.secret {
			t.Errorf("validSecret(%.20q)=%v want %v", c.in, got, c.secret)
		}
		if got := validToken(c.in); got != c.token && c.in != "" {
			t.Errorf("validToken(%.20q)=%v want %v", c.in, got, c.token)
		}
	}
	if validToken("") {
		t.Error("empty token must be rejected")
	}
}

func TestValidHTTPSURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/path?q=1":                true,
		"http://example.com":                          false,
		"https://":                                    false,
		"https://ex.com/a b":                          false,
		"https://ex.com/$(x)":                         false,
		"https://ex.com/'":                            false,
		"https://ex.com/`x`":                          false,
		"https://ex.com/\\":                           false,
		"ftp://ex.com":                                false,
		"https://ex.com/" + strings.Repeat("a", 2048): false,
	}
	for in, want := range cases {
		if got := validHTTPSURL(in); got != want {
			t.Errorf("validHTTPSURL(%.40q)=%v want %v", in, got, want)
		}
	}
}

func TestValidSSHPublicKeyAndCAKey(t *testing.T) {
	cases := []struct {
		in      string
		pub, ca bool
	}{
		{"ssh-ed25519 AAAAC3 comment", true, true},
		{"sk-ssh-ed25519@openssh.com AAAA", true, true},
		{"ssh-ed25519", false, false}, // missing trailing space/body
		{"", false, false},
		{"ssh-dss AAAA", false, false},
		{"ssh-ed25519 AAAA\nssh-rsa BBBB", false, false},
		{"ssh-ed25519 AAAA\x00", false, false},
		{"ssh-ed25519 AAAA " + strings.Repeat("a", 8192), false, false},
		{"ssh-ed25519 AAAA 'quoted'", true, false},
		{"ssh-ed25519 AAAA $(x)", true, false},
	}
	for _, c := range cases {
		if got := validSSHPublicKey(c.in); got != c.pub {
			t.Errorf("validSSHPublicKey(%.30q)=%v want %v", c.in, got, c.pub)
		}
		if got := validCAKey(c.in); got != c.ca {
			t.Errorf("validCAKey(%.30q)=%v want %v", c.in, got, c.ca)
		}
	}
}

func TestDigestEqual(t *testing.T) {
	if !digestEqual("abc", "abc") || digestEqual("abc", "abd") || digestEqual("abc", "ab") {
		t.Error("digestEqual mismatch")
	}
}
