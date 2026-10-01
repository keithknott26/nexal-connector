package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestServerSecretsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkServerSecretsDir(dir); err != nil {
		t.Fatalf("a private directory must be accepted: %v", err)
	}
	if err := checkServerSecretsDir("relative/dir"); err == nil {
		t.Fatal("relative path must be refused")
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := checkServerSecretsDir(dir); err == nil {
		t.Fatal("group access must be refused")
	}
	link := filepath.Join(t.TempDir(), "link")
	_ = os.Chmod(dir, 0o700)
	if err := os.Symlink(dir, link); err == nil {
		if err := checkServerSecretsDir(link); err == nil {
			t.Fatal("a symlink must be refused")
		}
	}
	if runtime.GOOS == "linux" {
		t.Setenv(ServerSecretsDirEnv, dir)
		s, err := NewSecrets(filepath.Join(dir, "..", "config.json"), Config{})
		if err != nil {
			t.Fatal(err)
		}
		if fs, ok := s.(FileSecrets); !ok || fs.Dir != dir {
			t.Fatalf("%T %+v", s, s)
		}
	}
}
