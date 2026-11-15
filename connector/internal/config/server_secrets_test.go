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

func TestNewSecretsDevelopmentFileStoreNeedsBothFlags(t *testing.T) {
	t.Setenv(ServerSecretsDirEnv, "")
	path := filepath.Join(t.TempDir(), "config.json")
	s, err := NewSecrets(path, Config{DevSecrets: true, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if fs, ok := s.(FileSecrets); !ok || fs.Dir != filepath.Join(filepath.Dir(path), "dev-secrets") {
		t.Fatalf("%T %+v", s, s)
	}
	if runtime.GOOS == "darwin" {
		return
	}
	// Either flag alone must not select file secrets: production refuses them.
	for name, c := range map[string]Config{"dev secrets only": {DevSecrets: true}, "development only": {Development: true}} {
		if s, err := NewSecrets(path, c); err == nil {
			t.Fatalf("%s: file secrets accepted outside explicit nonproduction mode: %T", name, s)
		}
	}
}

func TestNewSecretsRefusesUnconfiguredLinuxServer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux consults " + ServerSecretsDirEnv)
	}
	t.Setenv(ServerSecretsDirEnv, "")
	if s, err := NewSecrets(filepath.Join(t.TempDir(), "config.json"), Config{}); err == nil {
		t.Fatalf("expected refusal without a secrets directory, got %T", s)
	}
	t.Setenv(ServerSecretsDirEnv, "relative/dir")
	if s, err := NewSecrets(filepath.Join(t.TempDir(), "config.json"), Config{}); err == nil {
		t.Fatalf("expected refusal for an unsafe secrets directory, got %T", s)
	}
}
