package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSeedDirRejectsInvalidParamsWithoutCreatingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seed")
	p := testSeed()
	p.Hostname = "bad host!"
	if err := WriteSeedDir(dir, p); err == nil {
		t.Fatal("expected invalid seed to be rejected")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("seed dir must not be created for invalid params, stat err=%v", err)
	}
}

func TestHdiutilISORejectsNonISOPathBeforeTouchingFiles(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seed.img")
	if err := os.WriteFile(out, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := HdiutilISO(context.Background(), t.TempDir(), out)
	if err == nil || !strings.Contains(err.Error(), ".iso") {
		t.Fatalf("expected .iso suffix error, got %v", err)
	}
	if b, rerr := os.ReadFile(out); rerr != nil || string(b) != "keep" {
		t.Fatalf("existing file must be left alone: %q %v", b, rerr)
	}
}
