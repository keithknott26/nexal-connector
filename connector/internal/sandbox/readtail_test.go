package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTailReturnsOnlyTrailingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.log")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		max  int64
		want string
	}{
		{4, "6789"},
		{10, "0123456789"},
		{100, "0123456789"},
	} {
		got, err := readTail(path, c.max)
		if err != nil || got != c.want {
			t.Fatalf("readTail(max=%d) = %q, %v; want %q", c.max, got, err, c.want)
		}
	}
}

func TestReadTailMissingFileErrors(t *testing.T) {
	if _, err := readTail(filepath.Join(t.TempDir(), "absent.log"), 8); err == nil {
		t.Fatal("readTail of a missing file returned no error")
	}
}
