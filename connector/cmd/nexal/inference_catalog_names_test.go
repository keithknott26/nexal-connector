package main

import (
	"os"
	"path/filepath"
	"testing"

	"nexal/connector/internal/inference"
)

func TestCatalogNamesMapsShippedCatalogAndFallsBackOnBadPins(t *testing.T) {
	shipped, err := inference.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	check := func(label string, got map[string]string) {
		t.Helper()
		if len(got) == 0 {
			t.Fatalf("%s: no names", label)
		}
		for _, e := range shipped.Entries {
			if got[e.ID] != e.DisplayName {
				t.Fatalf("%s: %s = %q, want %q", label, e.ID, got[e.ID], e.DisplayName)
			}
		}
	}
	check("no pins", catalogNames(t.TempDir()))

	// A pins.json with loose permissions is rejected; names must still come from the shipped catalog.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pins.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("bad pins", catalogNames(dir))
}
