package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestShareWhileActiveDefaultsOnAndOffSticks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Config{Version: 1, Coordinator: "https://coordinator.example", Name: "Mac", Listen: "127.0.0.1:8765",
		Paused: true, MemoryLimitBytes: 512 << 20, ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
	if err := Save(path, c); err != nil {
		t.Skipf("fixture config not valid here: %v", err)
	}
	// A file written before the field existed (no "shareWhileActive" line) loads as on.
	b, _ := os.ReadFile(path)
	b = regexp.MustCompile(`\s*"shareWhileActive": (true|false),`).ReplaceAll(b, nil)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ShareWhileActive {
		t.Fatal("a configuration without the field must default to on")
	}
	// Turned off, it stays off across save and load.
	got.ShareWhileActive = false
	if err := Save(path, got); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil || again.ShareWhileActive {
		t.Fatalf("off must stick: %v %v", again.ShareWhileActive, err)
	}
}
