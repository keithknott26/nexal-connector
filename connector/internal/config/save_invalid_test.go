package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Save must validate before touching disk: an invalid config returns an error
// and leaves neither the target file nor its private directory behind.
func TestSaveRejectsInvalidConfigWithoutWriting(t *testing.T) {
	c := savedConfig()
	c.Version = 2
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "config.json")
	if err := Save(path, c); err == nil {
		t.Fatal("invalid config saved")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("invalid config touched disk: %v", err)
	}
}
