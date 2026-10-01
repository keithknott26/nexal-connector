package pool

import "testing"

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
