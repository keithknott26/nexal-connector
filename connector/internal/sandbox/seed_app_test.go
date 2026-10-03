package sandbox

import (
	"strings"
	"testing"
)

// An app profile must not change anything about how the host joins the mesh: it
// only adds its own env file, installer and unit, and the two runcmd lines that
// start it after the first-boot script.
func TestRenderUserDataHomeAssistantProfile(t *testing.T) {
	p := testSeed()
	p.AppProfile = AppProfileHomeAssistant
	u := RenderUserData(p)

	for _, want := range []string{
		"NEXAL_APP_PROFILE=home-assistant\n",
		"  - path: /etc/nexal/app.env\n",
		"  - path: /usr/local/sbin/nexal-app-setup\n",
		"    permissions: \"0755\"\n",
		"  - path: /etc/systemd/system/nexal-app.service\n",
		"  - [ systemctl, enable, nexal-app.service ]\n",
		"  - [ systemctl, start, --no-block, nexal-app.service ]\n",
		"ghcr.io/home-assistant/home-assistant:stable",
		`-p "$IP:8123:8123"`,
	} {
		if !strings.Contains(u, want) {
			t.Errorf("rendered user-data is missing %q", want)
		}
	}
	// The app is published on the mesh address alone, never on every interface.
	if strings.Contains(u, "--network host") || strings.Contains(u, "-p 8123:8123") {
		t.Error("Home Assistant must be bound to the mesh address only")
	}
	// It is started after the first boot has joined the mesh.
	if strings.Index(u, "nexal-first-boot") > strings.Index(u, "systemctl, enable, nexal-app.service") {
		t.Error("the app unit is started before the first-boot script")
	}
	if i := strings.Index(u, "runcmd:"); strings.Index(u, "/usr/local/sbin/nexal-app-setup") > i {
		t.Error("the installer must be in write_files, above runcmd")
	}
}

// Every line of an embedded file sits at the block scalar's indentation, or the
// YAML ends early and the seed is silently truncated.
func TestAppProfileBlockIsIndented(t *testing.T) {
	p := testSeed()
	p.AppProfile = AppProfileHomeAssistant
	lines := strings.Split(RenderUserData(p), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "  - path: /usr/local/sbin/nexal-app-setup") {
			start = i + 4 // path, owner, permissions, "content: |"
			break
		}
	}
	if start < 0 {
		t.Fatal("installer not rendered")
	}
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "  - path:") || strings.HasPrefix(line, "runcmd:") {
			return
		}
		if line != "" && !strings.HasPrefix(line, "      ") {
			t.Fatalf("script line is not indented into the block: %q", line)
		}
	}
}

func TestPlainImageRendersNoAppProfile(t *testing.T) {
	u := RenderUserData(testSeed())
	for _, unwanted := range []string{"NEXAL_APP_PROFILE", "nexal-app.service", "nexal-app-setup"} {
		if strings.Contains(u, unwanted) {
			t.Errorf("a plain image rendered %q", unwanted)
		}
	}
	p := testSeed()
	p.AppProfile = AppProfileNone
	if RenderUserData(p) != u {
		t.Error(`AppProfile "none" must render exactly like a plain image`)
	}
}

// An unknown profile is refused at both gates: the coordinator can be ahead of
// this runner, and a host that boots without its application is worse than none.
func TestUnknownAppProfileIsRefused(t *testing.T) {
	p := testSeed()
	p.AppProfile = "mystery-app"
	if err := ValidateSeed(p); err == nil {
		t.Error("ValidateSeed accepted an unknown app profile")
	}
	task := Task{Image: Image{AppProfile: "mystery-app"}}
	if err := validateV2(task); err == nil {
		t.Error("validateV2 accepted an unknown app profile")
	}
	for _, ok := range []string{"", AppProfileNone, AppProfileHomeAssistant} {
		if !KnownAppProfile(ok) {
			t.Errorf("KnownAppProfile(%q) = false", ok)
		}
	}
}
