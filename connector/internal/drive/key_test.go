package drive

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyPathSitsBesideConfig(t *testing.T) {
	got := KeyPath(filepath.Join("a", "b", "config.json"))
	if want := filepath.Join("a", "b", "drive-key.json"); got != want {
		t.Fatalf("KeyPath = %q, want %q", got, want)
	}
}

func TestLoadOrCreateKeyIsStableAcrossLoads(t *testing.T) {
	cfg := filepath.Join(privateDir(t), "config.json")
	first, err := LoadOrCreateKey(cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(KeyPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	second, err := LoadOrCreateKey(cfg)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("reloading produced a different key; existing objects would be orphaned")
	}
	if !bytes.Equal(PublicKey(first).Bytes(), PublicKey(second).Bytes()) {
		t.Fatal("public keys differ between loads")
	}
}

func TestLoadOrCreateKeyRefusesToOverwriteBadFiles(t *testing.T) {
	cases := map[string]string{
		"not json":        "garbage",
		"not an object":   `[1,2]`,
		"unknown field":   `{"version":1,"seed":"","createdAt":"x","extra":1}`,
		"future version":  `{"version":2,"seed":"","createdAt":"x"}`,
		"short seed":      `{"version":1,"seed":"AAAA","createdAt":"x"}`,
		"seed not base64": `{"version":1,"seed":"!!!!","createdAt":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := filepath.Join(privateDir(t), "config.json")
			path := KeyPath(cfg)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadOrCreateKey(cfg)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "drive:") {
				t.Fatalf("error %q lacks the drive: prefix", err)
			}
			got, rerr := os.ReadFile(path)
			if rerr != nil || string(got) != body {
				t.Fatalf("file was modified: %q (%v)", got, rerr)
			}
		})
	}
}

func TestLoadOrCreateKeyRefusesLoosePermissions(t *testing.T) {
	cfg := filepath.Join(privateDir(t), "config.json")
	if _, err := LoadOrCreateKey(cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(KeyPath(cfg))
	if err := os.Chmod(KeyPath(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(cfg); err == nil {
		t.Fatal("expected a world-readable key file to be refused")
	}
	after, _ := os.ReadFile(KeyPath(cfg))
	if !bytes.Equal(before, after) {
		t.Fatal("key file was replaced")
	}
}
