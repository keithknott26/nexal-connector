package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A missing private file must still satisfy errors.Is(ErrNotExist) (first startup),
// while the error text must not leak the configured path or the OS diagnostic.
func TestReadPrivateMissingKeepsNotExistWithoutLeakingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-dir-name", "missing.json")
	_, err := ReadPrivate(path, 1024)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("errors.Is(ErrNotExist) = false for %v", err)
	}
	if strings.Contains(err.Error(), "secret-dir-name") || strings.Contains(err.Error(), "no such file") {
		t.Fatalf("error leaks path or OS detail: %q", err.Error())
	}
}
