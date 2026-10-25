package sandbox

import (
	"errors"
	"testing"
)

func TestPermanentRequestError(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"unknown sandbox sb1", true},
		{"only a persistent sandbox can be stopped", true},
		{"stop not supported on this host", true},
		{"invalid sandbox id", true},
		{"sandbox is busy", false},
		{"provisioning in progress", false},
		{"", false},
	} {
		if got := permanentRequestError(errors.New(tc.msg)); got != tc.want {
			t.Errorf("permanentRequestError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
