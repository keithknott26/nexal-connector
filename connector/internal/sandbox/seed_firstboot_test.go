package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// firstBootScript finds the guest first-boot script (it lives in the platform
// repo next to this one). NEXAL_FIRSTBOOT_SCRIPT overrides the location.
func firstBootScript(t *testing.T) string {
	t.Helper()
	path := os.Getenv("NEXAL_FIRSTBOOT_SCRIPT")
	if path == "" {
		path = filepath.Join("..", "..", "..", "..", "nexal-platform", "sandbox-images", "first-boot", "nexal-first-boot")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("first-boot script not available (%v); set NEXAL_FIRSTBOOT_SCRIPT", err)
	}
	return string(b)
}

var envName = regexp.MustCompile(`NEXAL_[A-Z0-9_]+`)

// scriptEnvKeys returns (declared in the header "env keys" block, accepted by load_env).
func scriptEnvKeys(t *testing.T, script string) (declared, accepted map[string]bool) {
	t.Helper()
	declared, accepted = map[string]bool{}, map[string]bool{}
	inHeader := false
	for _, line := range strings.Split(script, "\n") {
		switch {
		case strings.HasPrefix(line, "# env keys:"):
			inHeader = true
		case strings.HasPrefix(line, "# optional:"):
			inHeader = false
		case strings.HasPrefix(line, "set -u"):
			inHeader = false
		}
		if inHeader && strings.HasPrefix(line, "#") {
			for _, k := range envName.FindAllString(line, -1) {
				declared[k] = true
			}
		}
		// the load_env case arm: NEXAL_A|NEXAL_B|...)
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "NEXAL_") && strings.Contains(t, "|") && strings.HasSuffix(t, ")") {
			for _, k := range strings.Split(strings.TrimSuffix(t, ")"), "|") {
				accepted[k] = true
			}
		}
	}
	if len(declared) == 0 || len(accepted) == 0 {
		t.Fatalf("could not parse the first-boot script (declared=%d accepted=%d)", len(declared), len(accepted))
	}
	return declared, accepted
}

// fullSeed sets every optional field so every key the seed can emit is present.
func fullSeed() SeedParams {
	p := testSeed()
	p.SSHCAPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIcaKey nexal-ca"
	p.DriveToken = "tok.en-1"
	p.Lifecycle = LifecyclePersistent
	p.DriveWritable = true
	p.ManagementURL = "https://mesh.example.net:443"
	p.DriveURL = "https://drive.example.net/"
	return p
}

// renderedEnv extracts KEY=VALUE lines of /etc/nexal/first-boot.env from the user-data.
func renderedEnv(t *testing.T, userData string) map[string]string {
	t.Helper()
	out := map[string]string{}
	in := false
	for _, line := range strings.Split(userData, "\n") {
		if strings.HasPrefix(line, "  - path: ") {
			in = line == "  - path: /etc/nexal/first-boot.env"
			continue
		}
		if !in || !strings.HasPrefix(line, "      NEXAL_") {
			continue
		}
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		out[k] = v
	}
	return out
}

// TestSeedSatisfiesFirstBoot keeps seed.go and nexal-first-boot in sync by
// parsing the script: every key the script declares as supplied by the seed is
// rendered, and every key the seed renders is one the script reads.
func TestSeedSatisfiesFirstBoot(t *testing.T) {
	script := firstBootScript(t)
	declared, accepted := scriptEnvKeys(t, script)
	env := renderedEnv(t, RenderUserData(fullSeed()))

	for k := range declared {
		if _, ok := env[k]; !ok {
			t.Errorf("first-boot declares %s but the seed does not render it", k)
		}
		if !accepted[k] {
			t.Errorf("first-boot declares %s but its load_env does not accept it", k)
		}
	}
	for k := range env {
		if !accepted[k] {
			t.Errorf("seed renders %s but first-boot ignores it (wrong name?)", k)
		}
	}
	// keys the task named explicitly (the CA is also written to a file first-boot falls back to)
	for _, k := range []string{"NEXAL_SSH_CA", "NEXAL_DRIVE_TOKEN", "NEXAL_DRIVE_MODE", "NEXAL_LIFECYCLE",
		"NEXAL_DRIVE_URL", "NEXAL_HOSTNAME", "NEXAL_SETUP_KEY", "NEXAL_VNC_PASSWORD", "NEXAL_DESKTOP", "NEXAL_MESH_URL"} {
		if env[k] == "" {
			t.Errorf("seed does not render a value for %s", k)
		}
	}
	// value shapes first-boot checks
	if m := env["NEXAL_DRIVE_MODE"]; m != "ro" && m != "rw" {
		t.Errorf("drive mode %q", m)
	}
	if l := env["NEXAL_LIFECYCLE"]; l != "persistent" && l != "ephemeral" {
		t.Errorf("lifecycle %q", l)
	}
	if len(env["NEXAL_VNC_PASSWORD"]) != 8 {
		t.Errorf("first-boot demands an 8-char VNC password, seed has %d", len(env["NEXAL_VNC_PASSWORD"]))
	}
	// the CA file path first-boot falls back to is the one the seed writes
	if !strings.Contains(script, "/etc/ssh/nexal_user_ca.pub") ||
		!strings.Contains(RenderUserData(fullSeed()), "path: /etc/ssh/nexal_user_ca.pub") {
		t.Error("CA file path differs between seed and first-boot")
	}
	// the default (minimal) seed still renders only keys first-boot reads
	for k := range renderedEnv(t, RenderUserData(testSeed())) {
		if !accepted[k] {
			t.Errorf("minimal seed renders %s but first-boot ignores it", k)
		}
	}
}

func TestFirstBootScriptReportsHostKey(t *testing.T) {
	script := firstBootScript(t)
	if n := strings.Count(script, `"hostKey":"%s"`); n < 2 {
		t.Errorf("first-boot must report hostKey on both the first and later boots, found %d", n)
	}
	if !strings.Contains(script, "ssh_host_ed25519_key.pub") {
		t.Error("hostKey must come from ssh_host_ed25519_key.pub")
	}
}
