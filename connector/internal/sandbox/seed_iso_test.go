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

func TestHdiutilISOWrapsBuilderFailureAndClearsStaleImage(t *testing.T) {
	if _, err := os.Stat("/usr/bin/hdiutil"); err == nil {
		t.Skip("hdiutil present; this test covers the missing-binary failure path")
	}
	out := filepath.Join(t.TempDir(), "seed.iso")
	if err := os.WriteFile(out, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := HdiutilISO(context.Background(), t.TempDir(), out)
	if err == nil || !strings.Contains(err.Error(), "building seed image failed") {
		t.Fatalf("expected wrapped builder failure, got %v", err)
	}
	if _, serr := os.Stat(out); !os.IsNotExist(serr) {
		t.Fatalf("stale seed image must be removed before building, stat err=%v", serr)
	}
}

func TestRemoveSeedEmptyArgsAreNoOpAndISOFailureIsReportedAfterDirRemoval(t *testing.T) {
	if err := RemoveSeed("", ""); err != nil {
		t.Fatalf("empty args must be a no-op: %v", err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "seed")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory in the image slot makes os.Remove fail with an error
	// that is not "not exist", which RemoveSeed must surface.
	iso := filepath.Join(root, "seed.iso")
	if err := os.MkdirAll(filepath.Join(iso, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSeed(dir, iso); err == nil {
		t.Fatal("expected an error when the seed image cannot be removed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("seed dir must still be removed when the image removal fails")
	}
}
