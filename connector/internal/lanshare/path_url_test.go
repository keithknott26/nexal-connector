package lanshare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSharePathRefusesMissingAndNonDirectory(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "nope")
	err := ValidateSharePath(missing)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing path: want a does-not-exist refusal, got %v", err)
	}

	file := filepath.Join(dir, "file.txt")
	if werr := os.WriteFile(file, []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	err = ValidateSharePath(file)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("regular file: want a not-a-directory refusal, got %v", err)
	}
}

func TestDestinationURLWithoutUsernameAndWithoutHost(t *testing.T) {
	got, err := DestinationURL("mac-mini.local", "", "Backups")
	if err != nil {
		t.Fatalf("DestinationURL: %v", err)
	}
	if got != "smb://mac-mini.local/Backups" {
		t.Errorf("got %q, want smb://mac-mini.local/Backups", got)
	}
	if _, err := DestinationURL("", "user", "Backups"); err == nil {
		t.Error("accepted an empty host")
	}
}
