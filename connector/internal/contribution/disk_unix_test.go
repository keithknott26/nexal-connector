//go:build linux || darwin

package contribution

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFreeDiskBytesReportsRealVolume(t *testing.T) {
	got, err := FreeDiskBytes(t.TempDir())
	if err != nil {
		t.Fatalf("FreeDiskBytes(tempdir) error: %v", err)
	}
	if got == 0 {
		t.Error("FreeDiskBytes(tempdir) = 0, want a non-zero amount on a writable temp volume")
	}
}

func TestFreeDiskBytesMissingPathDoesNotLeakPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "secret-dir-name", "nope")
	got, err := FreeDiskBytes(missing)
	if err == nil {
		t.Fatal("FreeDiskBytes(missing) error = nil, want an error")
	}
	if got != 0 {
		t.Errorf("FreeDiskBytes(missing) = %d, want 0 on error", got)
	}
	if strings.Contains(err.Error(), "secret-dir-name") {
		t.Errorf("error %q leaks the path", err)
	}
}
