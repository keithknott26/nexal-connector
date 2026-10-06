package agent

import "testing"

func TestOwnerOverrideFollowsShareWhileActive(t *testing.T) {
	a := &Agent{}
	if a.ownerOverrideLocked() {
		t.Fatal("override must be off by default")
	}
	a.cfg.ShareWhileActive = true
	if !a.ownerOverrideLocked() {
		t.Fatal("share-while-active must lift the owner-activity gate")
	}
	a.cfg.Paused = true
	if a.ownerOverrideLocked() {
		t.Fatal("a paused Mac never takes work, whatever the setting")
	}
}
