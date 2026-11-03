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
	for _, bad := range []string{`{`, `{"enabled":true,"maxSandboxes":1001}`, `{"maxSandboxes":-1}`, `{"placement":"everyone"}`} {
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
		t.Fatalf("missing file must default to disabled without error: %+v %v", c, err)
	}
	if err := os.WriteFile(p, []byte(`{"enabled":true,"maxSandboxes":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = LoadHostingConfig(p)
	if err != nil || !c.Enabled || c.MaxSandboxes != 9 {
		t.Fatalf("explicit opt-in must be honored: %+v %v", c, err)
	}
	if err := os.WriteFile(p, []byte(`{"maxSandboxes":1001}`), 0o600); err != nil {
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

func TestHostingConfigManagedLimitAndOversizedBody(t *testing.T) {
	if _, err := ParseHostingConfig([]byte(`{"managed":true,"maxSandboxes":64}`)); err != nil {
		t.Fatalf("managed server at its cap must be accepted: %v", err)
	}
	if _, err := ParseHostingConfig([]byte(`{"managed":true,"maxSandboxes":65}`)); err == nil {
		t.Fatal("managed server above MaxManagedSandboxes must be rejected")
	}
	if _, err := ParseHostingConfig([]byte(`{"maxSandboxes":65}`)); err != nil {
		t.Fatalf("unmanaged Mac may exceed the managed cap: %v", err)
	}
	big := make([]byte, 16<<10+1)
	for i := range big {
		big[i] = ' '
	}
	if _, err := ParseHostingConfig(big); err == nil {
		t.Fatal("config over 16 KiB must be rejected")
	}
}

func TestHostingConfigManagedCapsRaisesBudget(t *testing.T) {
	plain := HostingConfig{Enabled: true, MaxSandboxes: 2, Placement: PlacementMembers}.Caps()
	managed := HostingConfig{Enabled: true, Managed: true, MaxSandboxes: 2, Placement: PlacementMembers}.Caps()
	if managed.MaxCPUFraction <= plain.MaxCPUFraction || managed.MaxMemFraction <= plain.MaxMemFraction {
		t.Fatalf("managed caps should allow a larger share: plain=%+v managed=%+v", plain, managed)
	}
	if !managed.StrictCPU {
		t.Fatalf("managed caps must keep the CPU budget strict: %+v", managed)
	}
}
