package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runWith(t *testing.T, args ...string) error {
	t.Helper()
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{"nexal-validation-lab"}, args...)
	return run()
}

func writeCfg(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lab.json")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("error = %v, want containing %q", err, sub)
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	wantErr(t, runWith(t), "usage")
	wantErr(t, runWith(t, "a", "b"), "usage")
	if err := runWith(t, filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
	wantErr(t, runWith(t, t.TempDir()), "private regular file")
	wantErr(t, runWith(t, writeCfg(t, `{}`, 0o644)), "private regular file")
	wantErr(t, runWith(t, writeCfg(t, `not json`, 0o600)), "invalid configuration")
	wantErr(t, runWith(t, writeCfg(t, `{"coordinator":"https://example.invalid","hostId":"host-1"}`, 0o600)), "invalid lab enrollment")
	wantErr(t, runWith(t, writeCfg(t, `{"coordinator":"https://example.invalid","hostId":"../x","token":"t"}`, 0o600)), "invalid lab enrollment")
}

func TestRunRejectsSymlinkConfig(t *testing.T) {
	target := writeCfg(t, `{}`, 0o600)
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	wantErr(t, runWith(t, link), "private regular file")
}
