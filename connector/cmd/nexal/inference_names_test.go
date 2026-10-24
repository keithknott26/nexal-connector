package main

import "testing"

func TestValidMeshName(t *testing.T) {
	for name, want := range map[string]bool{
		"mac-mini-m4.e8gas.mesh.nexal.systems":  true,
		"mac-mini-m4.peers.mesh.nexal.systems":  true,
		"":                                      false,
		"evil.example.com":                      false,
		"-bad.e8gas.mesh.nexal.systems":         false,
		"UPPER.e8gas.mesh.nexal.systems":        false,
		"a b.e8gas.mesh.nexal.systems":          false,
		"x.mesh.nexal.systems.attacker.example": false,
	} {
		if got := validMeshName(name); got != want {
			t.Errorf("validMeshName(%q) = %v", name, got)
		}
	}
}

func TestMeshOpened(t *testing.T) {
	if !meshOpened([]string{"192.168.68.56", "100.86.56.136"}) {
		t.Error("a neXal address is answering")
	}
	if meshOpened([]string{"192.168.68.56", "127.0.0.1"}) {
		t.Error("no neXal address: the name would point nowhere that answers")
	}
}
