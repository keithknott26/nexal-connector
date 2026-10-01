package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseHostingConfig(t *testing.T) {
	c, err := ParseHostingConfig([]byte(`{"enabled":true,"maxSandboxes":3,"placement":"owner","extra":1}`))
	if err != nil || !c.Enabled || c.MaxSandboxes != 3 || c.Placement != "owner" {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = ParseHostingConfig([]byte(`{"enabled":true}`))
	if err != nil || c.MaxSandboxes != 5 || c.Placement != "members" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for alias, want := range map[string]string{"any": "members", "mine": "owner"} {
		c, err = ParseHostingConfig([]byte(`{"enabled":true,"placement":"` + alias + `"}`))
		if err != nil || c.Placement != want {
			t.Fatalf("alias %s: %+v %v", alias, c, err)
		}
	}
	for _, bad := range []string{`{`, `{"enabled":true,"maxSandboxes":11}`, `{"maxSandboxes":-1}`, `{"placement":"everyone"}`} {
		if _, err := ParseHostingConfig([]byte(bad)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

func TestLoadHostingConfigFailsClosed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sandbox-hosting.json")
	c, err := LoadHostingConfig(p)
	if err != nil || c.Enabled {
		t.Fatalf("missing file must be disabled without error: %+v %v", c, err)
	}
	if err := os.WriteFile(p, []byte(`{"enabled":true,"maxSandboxes":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = LoadHostingConfig(p)
	if err == nil || c.Enabled {
		t.Fatalf("invalid file must be disabled with an error: %+v %v", c, err)
	}
}

func TestHostingConfigCaps(t *testing.T) {
	k := HostingConfig{Enabled: true, MaxSandboxes: 2, Placement: "members"}.Caps()
	if !k.Enabled || k.MaxSandboxes != 2 || !k.AllowOnBattery {
		t.Fatalf("%+v", k)
	}
}

func TestConfigWatcher(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	w := &configWatcher{path: p}
	if _, ch, _ := w.Changed(); !ch {
		t.Fatal("first call reports a change")
	}
	if _, ch, _ := w.Changed(); ch {
		t.Fatal("unchanged missing file")
	}
	if err := os.WriteFile(p, []byte(`{"enabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, ch, err := w.Changed()
	if !ch || err != nil || !c.Enabled {
		t.Fatalf("%+v %v %v", c, ch, err)
	}
	if _, ch, _ := w.Changed(); ch {
		t.Fatal("unchanged file")
	}
}
