package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withStdin(t *testing.T, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old; f.Close() })
}

func TestReadBoundedStdinAcceptsInputAtLimit(t *testing.T) {
	withStdin(t, "12345")
	b, err := readBoundedStdin(5)
	if err != nil || string(b) != "12345" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestReadBoundedStdinRefusesOneByteOverLimit(t *testing.T) {
	withStdin(t, "123456")
	b, err := readBoundedStdin(5)
	if err == nil || !strings.Contains(err.Error(), "exceeds 5 bytes") || b != nil {
		t.Fatalf("got %q, %v; want refusal rather than truncation", b, err)
	}
}

func TestReadReviewedRejectsMissingDirectoryAndGarbage(t *testing.T) {
	dir := t.TempDir()
	if _, err := readReviewed(filepath.Join(dir, "none.json")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := readReviewed(dir); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("directory: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readReviewed(bad); err == nil {
		t.Fatal("garbage pins file accepted")
	}
}
