//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerUIDReportsCurrentUserForOwnFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := ownerUID(fi)
	if !ok {
		t.Fatal("ownerUID not available for a real file on unix")
	}
	if uid != os.Getuid() {
		t.Fatalf("ownerUID = %d, want %d", uid, os.Getuid())
	}
}
