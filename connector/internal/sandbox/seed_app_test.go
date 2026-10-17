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
	for _, ok := range []string{"", AppProfileNone, AppProfileHomeAssistant, AppProfileJellyfin} {
		if !KnownAppProfile(ok) {
			t.Errorf("KnownAppProfile(%q) = false", ok)
		}
	}
}

func TestRenderUserDataJellyfinProfile(t *testing.T) {
	p := testSeed()
	p.AppProfile = AppProfileJellyfin
	u := RenderUserData(p)
	for _, want := range []string{
		"NEXAL_APP_PROFILE=jellyfin\n",
		"docker.io/jellyfin/jellyfin:latest",
		`-p "$IP:8096:8096"`,
		"target=/media/shared-drive,readonly,bind-propagation=rslave",
		"wakeonlan etherwake",
		// Debian 13 packages the docker client separately and the install skips recommends.
		"install -y -qq --no-install-recommends docker-cli",
		// Progress a person can follow: what is happening, what comes next, and a download heartbeat.
		`plan 3 5 "downloading Jellyfin`,
		"Next: $4.",
		"still downloading $1",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("rendered user-data is missing %q", want)
		}
	}
	if strings.Contains(u, "-p 8096:8096") || strings.Contains(u, "--network host") {
		t.Error("Jellyfin must be bound to the mesh address only")
	}
}

// Kali is an app profile with a desktop: the installer must be the Kali one, the desktop is
// bound to the mesh address only, and the profile is one this runner accepts.
func TestRenderUserDataKaliProfile(t *testing.T) {
	p := testSeed()
	p.AppProfile = AppProfileKali
	if err := ValidateSeed(p); err != nil {
		t.Fatalf("kali must be a known profile: %v", err)
	}
	u := RenderUserData(p)
	for _, want := range []string{
		"NEXAL_APP_PROFILE=kali\n",
		"kali-rolling main contrib non-free non-free-firmware",
		"apt_install --allow-downgrades libexpat1",
		"mv \"$f\" /root/apt-debian/",
		"exit 101",
		"kali-linux-default kali-desktop-xfce",
		"-interface $IP -rfbauth /home/nexal/.vnc/passwd",
		"  - [ systemctl, start, --no-block, nexal-app.service ]\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("rendered user-data is missing %q", want)
		}
	}
	// first-boot.env is deleted by the first-boot script, so the Kali profile carries its own copy of
	// the desktop flag and VNC password in a root-only app.env, and the installer reads it from there.
	i := strings.Index(u, "  - path: /etc/nexal/app.env\n")
	if i < 0 {
		t.Fatal("app.env not rendered")
	}
	appEnv := u[i : i+400]
	for _, want := range []string{"permissions: \"0600\"", "NEXAL_DESKTOP=1\n", "NEXAL_VNC_PASSWORD=" + p.VNCPassword + "\n"} {
		if !strings.Contains(appEnv, want) {
			t.Errorf("app.env is missing %q:\n%s", want, appEnv)
		}
	}
	if strings.Contains(u, "/etc/nexal/first-boot.env 2>/dev/null") {
		t.Error("the Kali installer must not read first-boot.env (it is deleted after first boot)")
	}
	if strings.Contains(u, "-localhost no") || strings.Contains(u, "-interface 0.0.0.0") {
		t.Error("the Kali desktop must be bound to the mesh address only")
	}
}

func TestRenderUserDataDesktopProfile(t *testing.T) {
	p := testSeed()
	p.AppProfile = AppProfileDesktop
	p.Desktop = true
	p.VNCPassword = "pw123456"
	if err := ValidateSeed(p); err != nil {
		t.Fatal(err)
	}
	u := RenderUserData(p)
	for _, want := range []string{
		"NEXAL_APP_PROFILE=desktop\n",
		"NEXAL_VNC_PASSWORD=pw123456\n",
		"apt_install --no-install-recommends xfce4 xfce4-terminal xorg dbus-x11 tigervnc-standalone-server",
		"log \"apt: ",
		"-interface $IP -rfbauth /home/nexal/.vnc/passwd",
		"  - [ systemctl, start, --no-block, nexal-app.service ]\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("desktop seed missing %q", want)
		}
	}
	if strings.Contains(u, "0.0.0.0") {
		t.Error("desktop must bind to the mesh address only")
	}
}

func TestRenderUserDataInstallsGuestAgent(t *testing.T) {
	u := RenderUserData(testSeed())
	for _, want := range []string{
		"path: /usr/local/sbin/nexal-guest-agent\n",
		"DEV = \"/dev/hvc1\"",
		"path: /etc/systemd/system/nexal-guest-agent.service\n",
		"systemctl enable --now nexal-guest-agent.service",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("seed missing %q", want)
		}
	}
}
