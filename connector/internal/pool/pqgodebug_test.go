package pool

import (
	"os"
	"testing"
)

func TestStrippedPostQuantumGODEBUG(t *testing.T) {
	cases := []struct {
		in, out string
		removed bool
	}{
		{"", "", false},
		{"http2client=0", "http2client=0", false},
		{"tlsmlkem=0,tlssecpmlkem=0", "", true},
		{"http2client=0,tlssecpmlkem=0,x509sha1=1", "http2client=0,x509sha1=1", true},
		{"tlsmlkem=1", "tlsmlkem=1", false},
	}
	for _, c := range cases {
		got, removed := StrippedPostQuantumGODEBUG(c.in)
		if got != c.out || removed != c.removed {
			t.Errorf("%q: got %q,%v want %q,%v", c.in, got, removed, c.out, c.removed)
		}
	}
}

func TestEnforcePostQuantumGODEBUG(t *testing.T) {
	t.Run("unset is a no-op", func(t *testing.T) {
		t.Setenv("GODEBUG", "x")
		if err := os.Unsetenv("GODEBUG"); err != nil {
			t.Fatal(err)
		}
		if EnforcePostQuantumGODEBUG() {
			t.Fatal("reported a change with GODEBUG unset")
		}
		if _, ok := os.LookupEnv("GODEBUG"); ok {
			t.Fatal("GODEBUG was set by a no-op")
		}
	})
	t.Run("nothing to strip leaves it alone", func(t *testing.T) {
		t.Setenv("GODEBUG", "http2client=0,tlsmlkem=1")
		if EnforcePostQuantumGODEBUG() {
			t.Fatal("reported a change when nothing disables PQ")
		}
		if got := os.Getenv("GODEBUG"); got != "http2client=0,tlsmlkem=1" {
			t.Fatalf("GODEBUG = %q, want unchanged", got)
		}
	})
	t.Run("strips PQ switches and keeps the rest", func(t *testing.T) {
		t.Setenv("GODEBUG", "http2client=0,tlsmlkem=0,x509sha1=1")
		if !EnforcePostQuantumGODEBUG() {
			t.Fatal("expected a change")
		}
		if got := os.Getenv("GODEBUG"); got != "http2client=0,x509sha1=1" {
			t.Fatalf("GODEBUG = %q", got)
		}
		if EnforcePostQuantumGODEBUG() {
			t.Fatal("second call must be idempotent")
		}
	})
	t.Run("only PQ switches unsets the variable", func(t *testing.T) {
		t.Setenv("GODEBUG", "tlsmlkem=0,tlssecpmlkem=0")
		if !EnforcePostQuantumGODEBUG() {
			t.Fatal("expected a change")
		}
		if v, ok := os.LookupEnv("GODEBUG"); ok {
			t.Fatalf("GODEBUG still set to %q", v)
		}
	})
}
