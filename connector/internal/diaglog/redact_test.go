package diaglog

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bearer", "Authorization: Bearer abc.def-123", "Authorization: Bearer [redacted]"},
		{"url userinfo", "dial https://user:pw@host/x", "dial https://[redacted]@host/x"},
		{"json secret", `{"api_key":"hunter2"}`, `{"api_key"` + `:"[redacted]"}`},
		{"kv password", "password=hunter2 next", `password="[redacted]" next`},
		{"prefixed token", "got ghp_abcdefgh12345678 ok", "got [redacted] ok"},
		{"mac", "iface AA:bb:CC:dd:EE:ff up", "iface [mac] up"},
		{"private key", "x -----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY----- y", "x [redacted private key] y"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("%s: Redact(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestRedactKeepsHarmlessText(t *testing.T) {
	for _, in := range []string{
		"token=true",
		"password: null",
		"secret=12345",
		"plain log line with no secrets",
		strings.Repeat("a", 40), // long run without digits is not key-like
	} {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactLongMixedRun(t *testing.T) {
	in := "key " + strings.Repeat("aB3", 12) + " end"
	if got := Redact(in); got != "key [redacted] end" {
		t.Errorf("got %q", got)
	}
}

func TestRedactKeepsHarmlessSecretFieldValues(t *testing.T) {
	for _, in := range []string{
		`{"token":true}`,
		`{"token":null}`,
		`{"password":""}`,
		`token=42`,
		`secret_ttl=3.5`,
		`{"api_key":"[redacted]"}`,
	} {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactLongRun(t *testing.T) {
	mixed := strings.Repeat("aB3", 12) // 36 chars, letters and digits
	if got := Redact("key " + mixed + " end"); got != "key [redacted] end" {
		t.Errorf("mixed long run not redacted: %q", got)
	}
	digits := strings.Repeat("7", 40)
	if got := Redact(digits); got != digits {
		t.Errorf("digit-only run should be kept: %q", got)
	}
	letters := strings.Repeat("a", 40)
	if got := Redact(letters); got != letters {
		t.Errorf("letter-only run should be kept: %q", got)
	}
}
