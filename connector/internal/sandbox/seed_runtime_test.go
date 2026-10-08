package sandbox

import (
	"strings"
	"testing"
)

func runtimeSeed() SeedParams {
	p := SeedParams{SandboxID: "sb1", InstanceID: "sb1-1", Hostname: "sbx-1", SetupKey: "KEY-abc",
		ManagementURL: "https://mesh.example.com", MeshImage: "ghcr.io/keithknott26/nexal-mesh-sidecar:0.1.7"}
	return p
}

// Stock cloud images have no neXal first-boot script or runtime: the seed must
// bring both, and report on hvc0 (the console the runner reads), not only
// /dev/console (tty0/ttyAMA0 on those images, never read).
func TestSeedBringsItsOwnFirstBoot(t *testing.T) {
	u := RenderUserData(runtimeSeed())
	for _, want := range []string{
		"  - path: " + seedFirstBootPath,
		"  - path: " + fetchRuntimePath,
		"  - path: " + meshRuntimeEnvPath,
		"      NEXAL_MESH_IMAGE=ghcr.io/keithknott26/nexal-mesh-sidecar:0.1.7",
		"tee /dev/hvc0",
		"S=" + seedFirstBootPath,
		"--enable-rosenpass",
		"NEXAL-FIRSTBOOT-FAILED",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("user-data lacks %q", want)
		}
	}
	runcmd := u[strings.Index(u, "runcmd:"):]
	if strings.Contains(runcmd, "> /dev/console 2>&1; else") {
		t.Error("runcmd still reports only on /dev/console")
	}
	if i := strings.Index(u, "runcmd:"); strings.Index(u, "  - path: "+seedFirstBootPath) > i {
		t.Error("the seed first-boot script must be in write_files, above runcmd")
	}
}

// The runtime image is not a secret and must not appear in first-boot.env (whose
// keys are kept in sync with the platform's nexal-first-boot).
func TestMeshImageStaysOutOfFirstBootEnv(t *testing.T) {
	u := RenderUserData(runtimeSeed())
	start := strings.Index(u, "/etc/nexal/first-boot.env")
	end := strings.Index(u[start:], "  - path:")
	if strings.Contains(u[start:start+end], "NEXAL_MESH_IMAGE") {
		t.Error("NEXAL_MESH_IMAGE leaked into first-boot.env")
	}
}

func TestValidateSeedMeshImage(t *testing.T) {
	for _, ok := range []string{"", "ghcr.io/keithknott26/nexal-mesh-sidecar:0.1.7", "localhost:5000/a/b:dev"} {
		p := runtimeSeed()
		p.MeshImage = ok
		if err := ValidateSeed(p); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"ghcr.io/x/y", "https://ghcr.io/x/y:1", "ghcr.io/x/y:1\nNEXAL_SETUP_KEY=z", "ghcr.io/x/y:1;rm", "ghcr.io/x/y@sha256:ab"} {
		p := runtimeSeed()
		p.MeshImage = bad
		if ValidateSeed(p) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
