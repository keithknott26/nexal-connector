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

func TestMeshOpenedRangeBoundaries(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.64.0.0":      true,
		"100.127.255.255": true,
		"100.63.255.255":  false,
		"100.128.0.0":     false,
		"not-an-ip":       false,
		"":                false,
		"::1":             false,
	} {
		if got := meshOpened([]string{addr}); got != want {
			t.Errorf("meshOpened(%q) = %v, want %v", addr, got, want)
		}
	}
	if meshOpened(nil) {
		t.Error("no addresses answering must not report the mesh as open")
	}
}
