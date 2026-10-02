package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMeshRuntimeBuildsAligned keeps every mesh-runtime build on ONE version and
// ONE patch set. On 2026-10-01 the Mac (gated-experimental.7), the gateway
// (gated.8) and the sidecar (gated.9) ran different patch levels; the newer
// eligibility rule then skipped the older peers and their ML-KEM keys expired
// hundreds of times. experiments/mlkem1024-mesh/RUNTIME_VERSION is the source of
// truth; nexal-platform syncs its gateway copy from that directory.
func TestMeshRuntimeBuildsAligned(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	mesh := filepath.Join(repo, "experiments", "mlkem1024-mesh")
	raw, err := os.ReadFile(filepath.Join(mesh, "RUNTIME_VERSION"))
	if err != nil {
		t.Skip(err)
	}
	version := strings.TrimSpace(string(raw))
	if !regexp.MustCompile(`^0\.79\.0-nexal-mlkem1024-gated\.[0-9]+$`).MatchString(version) {
		t.Fatalf("RUNTIME_VERSION %q is not a gated release string", version)
	}

	dockerfile, err := os.ReadFile(filepath.Join(repo, "connector", "sidecar", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	args := regexp.MustCompile(`(?m)^ARG RUNTIME_VERSION=(\S+)$`).FindAllStringSubmatch(string(dockerfile), -1)
	if len(args) == 0 {
		t.Fatal("sidecar Dockerfile declares no RUNTIME_VERSION")
	}
	for _, a := range args {
		if a[1] != version+"-linux" {
			t.Errorf("sidecar Dockerfile RUNTIME_VERSION=%s, want %s-linux", a[1], version)
		}
	}

	update, err := os.ReadFile(filepath.Join(repo, "macos", "scripts", "update-runtime.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(update), `"$PATCHES/RUNTIME_VERSION"`) {
		t.Error("update-runtime.sh must take its version from RUNTIME_VERSION")
	}

	prepare, err := os.ReadFile(filepath.Join(mesh, "prepare.py"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(prepare)
	// Every patch and every test in the directory must actually be used.
	entries, _ := os.ReadDir(mesh)
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, "-mlkem1024.patch"):
			// Applied through the f'{name}-mlkem1024.patch' loop.
			if !strings.Contains(src, "-mlkem1024.patch") {
				t.Errorf("prepare.py does not apply %s", name)
			}
		case strings.HasSuffix(name, ".patch"), strings.HasSuffix(name, "_test.go") && !strings.HasPrefix(name, "nexal_"):
			if !strings.Contains(src, name) {
				t.Errorf("prepare.py does not use %s", name)
			}
		}
	}
	// The Linux empty-batch crash guard must stay in the build.
	if strings.Count(src, "nexal: empty batch") < 2 {
		t.Error("prepare.py lost the empty-batch send guard (ICEBind.Send and StdNetBind.Send)")
	}
	// The staged-packet race fix (pinned fork 8bf8fa9) must stay in the build.
	if !strings.Contains(src, "nexal: count before the send") {
		t.Error("prepare.py lost the StagePackets race fix")
	}
	if !strings.Contains(string(dockerfile), "nexal: empty batch") {
		t.Error("the sidecar image build no longer verifies the empty-batch guard")
	}
}
