package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSeed() SeedParams {
	return SeedParams{
		SandboxID:     "sbx_1",
		InstanceID:    "sbx_1-100",
		Hostname:      "box-one",
		SetupKey:      "KEY-123.abc",
		VNCPassword:   "pw0rdPW9",
		SSHPublicKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample alice@laptop"},
		Desktop:       true,
	}
}

func TestRenderUserData(t *testing.T) {
	u := RenderUserData(testSeed())
	for _, want := range []string{
		"#cloud-config\n",
		"hostname: \"box-one\"",
		"ssh_pwauth: false",
		"name: nexal",
		"NOPASSWD:ALL",
		"- \"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample alice@laptop\"",
		"PasswordAuthentication no",
		"NEXAL_SETUP_KEY=KEY-123.abc",
		"NEXAL_VNC_PASSWORD=pw0rdPW9",
		"NEXAL_SSH_MESH_ONLY=1",
		"NEXAL_VNC_MESH_ONLY=1",
		"NEXAL_DESKTOP=1",
		"nexal drive mount --read-only /mnt/nexal-drive",
		"/etc/nexal/first-boot.env",
		"permissions: \"0600\"",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("user-data missing %q\n%s", want, u)
		}
	}
}

func TestRenderUserDataDriveWritable(t *testing.T) {
	p := testSeed()
	p.DriveWritable = true
	if !strings.Contains(RenderUserData(p), "--read-write") {
		t.Fatal("writable drive not rendered")
	}
}

func TestRenderMetaData(t *testing.T) {
	m := RenderMetaData(testSeed())
	if m != "instance-id: sbx_1-100\nlocal-hostname: box-one\n" {
		t.Fatalf("unexpected meta-data %q", m)
	}
}

func TestYAMLQuotingBlocksInjection(t *testing.T) {
	got := yq("a\"b\nc: d")
	if strings.Contains(got, "\n") || got != `"a\"b\nc: d"` {
		t.Fatalf("bad quoting: %s", got)
	}
}

func TestValidateSeedRejects(t *testing.T) {
	bad := []func(*SeedParams){
		func(p *SeedParams) { p.Hostname = "Bad Host" },
		func(p *SeedParams) { p.SetupKey = "k\nINJECT=1" },
		func(p *SeedParams) { p.VNCPassword = "a b" },
		func(p *SeedParams) { p.SSHPublicKeys = []string{"not a key"} },
		func(p *SeedParams) { p.SSHPublicKeys = []string{"ssh-ed25519 AAA\nssh-rsa BBB"} },
		func(p *SeedParams) { p.SandboxID = "../x" },
		func(p *SeedParams) { p.SetupKey = "" },
	}
	for i, mut := range bad {
		p := testSeed()
		mut(&p)
		if err := ValidateSeed(p); err == nil {
			t.Errorf("case %d: expected rejection", i)
		}
	}
	if err := ValidateSeed(testSeed()); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAndRemoveSeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seed")
	if err := WriteSeedDir(dir, testSeed()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"user-data", "meta-data"} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", f, fi.Mode().Perm())
		}
	}
	iso := filepath.Join(filepath.Dir(dir), "seed.iso")
	if err := os.WriteFile(iso, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSeed(dir, iso); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("seed dir not removed")
	}
	if _, err := os.Stat(iso); !os.IsNotExist(err) {
		t.Error("seed iso not removed")
	}
	if err := RemoveSeed(dir, iso); err != nil {
		t.Errorf("second removal must be a no-op: %v", err)
	}
}

func TestParseFirstBootLine(t *testing.T) {
	fp := "SHA256:" + strings.Repeat("A", 43)
	fb, ok, failed, _ := ParseFirstBootLine(`[ 12.3] NEXAL-FIRSTBOOT {"meshIp":"100.64.1.2","hostKeyFingerprint":"` + fp + `"}` + "\r")
	if !ok || failed || fb.MeshIP != "100.64.1.2" || fb.HostKeyFingerprint != fp {
		t.Fatalf("unexpected %+v ok=%v failed=%v", fb, ok, failed)
	}
	if _, ok, _, _ := ParseFirstBootLine(`NEXAL-FIRSTBOOT {"meshIp":"not-an-ip"}`); ok {
		t.Fatal("bad ip accepted")
	}
	if _, ok, _, _ := ParseFirstBootLine(`NEXAL-FIRSTBOOT {"meshIp":"100.64.1.2","hostKeyFingerprint":"x; rm -rf"}`); ok {
		t.Fatal("bad fingerprint accepted")
	}
	_, ok, failed, why := ParseFirstBootLine("NEXAL-FIRSTBOOT-FAILED mesh join refused")
	if ok || !failed || why != "mesh join refused" {
		t.Fatalf("failure line: ok=%v failed=%v why=%q", ok, failed, why)
	}
	if _, ok, failed, _ := ParseFirstBootLine("random console noise"); ok || failed {
		t.Fatal("noise misparsed")
	}
}

func TestScanFirstBootNewestWins(t *testing.T) {
	text := "NEXAL-FIRSTBOOT-FAILED old\nNEXAL-FIRSTBOOT {\"meshIp\":\"100.64.0.9\"}\nnoise\n"
	fb, ok, failed, _ := scanFirstBoot(text)
	if !ok || failed || fb.MeshIP != "100.64.0.9" {
		t.Fatalf("got %+v %v %v", fb, ok, failed)
	}
}
