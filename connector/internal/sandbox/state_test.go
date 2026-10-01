package sandbox

import "testing"

func TestTransitions(t *testing.T) {
	ok := [][2]State{
		{"", StateProvisioning},
		{StateProvisioning, StateRunning},
		{StateProvisioning, StateFailed},
		{StateRunning, StateStopping},
		{StateRunning, StateProvisioning},
		{StateFailed, StateStopping},
		{StateStopping, StateDeleted},
	}
	for _, c := range ok {
		if !CanTransition(c[0], c[1]) {
			t.Errorf("%q -> %q should be allowed", c[0], c[1])
		}
	}
	bad := [][2]State{
		{"", StateRunning},
		{StateDeleted, StateRunning},
		{StateDeleted, StateProvisioning},
		{StateStopping, StateRunning},
		{StateRunning, StateDeleted},
		{StateRunning, StateRunning},
	}
	for _, c := range bad {
		if CanTransition(c[0], c[1]) {
			t.Errorf("%q -> %q should be refused", c[0], c[1])
		}
	}
}

func TestActive(t *testing.T) {
	if !StateRunning.Active() || !StateProvisioning.Active() || !StateStopping.Active() {
		t.Fatal("running, provisioning, stopping hold resources")
	}
	if StateFailed.Active() || StateDeleted.Active() {
		t.Fatal("failed and deleted hold none")
	}
}
