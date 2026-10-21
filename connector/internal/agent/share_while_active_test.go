package agent

import (
	"testing"

	"nexal/connector/internal/config"
)

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

func TestSetShareWhileActivePersistsAndFences(t *testing.T) {
	a, _ := testAgent(t)

	if err := a.SetShareWhileActive(true); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.ShareWhileActive || !a.cfg.ShareWhileActive {
		t.Fatal("enabling must persist and apply the setting")
	}
	select {
	case <-a.heartbeatWake:
	default:
		t.Fatal("a change must wake the heartbeat so the coordinator hears at once")
	}

	// Turning it on needs no fence, so state is untouched; turning it off fences.
	gen := a.stateGeneration
	if err := a.SetShareWhileActive(true); err != nil || a.stateGeneration != gen {
		t.Fatalf("no-op change must not fence: err=%v gen %d -> %d", err, gen, a.stateGeneration)
	}
	if err := a.SetShareWhileActive(false); err != nil {
		t.Fatal(err)
	}
	if a.stateGeneration == gen {
		t.Fatal("withdrawing consent must advance the state generation")
	}
	if saved, _ = config.Load(a.path); saved.ShareWhileActive {
		t.Fatal("disabling must be persisted")
	}
}
