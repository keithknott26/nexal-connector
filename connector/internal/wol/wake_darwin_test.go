//go:build darwin

package wol

import (
	"errors"
	"testing"
)

func TestWakeForNetworkDarwin(t *testing.T) {
	prev := pmsetCustom
	defer func() { pmsetCustom = prev }()
	pmsetCustom = func() ([]byte, error) { return []byte("AC Power:\n womp 1\n"), nil }
	if !WakeForNetwork() {
		t.Fatal("womp 1 not detected")
	}
	pmsetCustom = func() ([]byte, error) { return []byte("AC Power:\n womp 1\n"), errors.New("exit 1") }
	if WakeForNetwork() {
		t.Fatal("failed pmset must read as false")
	}
}
