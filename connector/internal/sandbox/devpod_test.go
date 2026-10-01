package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRun struct {
	mu       sync.Mutex
	cmds     []string
	devpodUp error
	logs     string
}

func (f *fakeRun) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.cmds = append(f.cmds, line)
	f.mu.Unlock()
	switch {
	case strings.Contains(line, "version --format"):
		return []byte("27.0"), nil
	case strings.Contains(line, " up ") && strings.Contains(line, "devpod"):
		if f.devpodUp != nil {
			return []byte("boom"), f.devpodUp
		}
		return nil, nil
	case strings.Contains(line, "ps -q --filter label="):
		return []byte("cid123\n"), nil
	case strings.Contains(line, "ps -aq"):
		return []byte("cid123\n"), nil
	case strings.Contains(line, "ps -q --filter name=nexal-mesh"):
		return []byte("side1\n"), nil
	case strings.Contains(line, " logs "):
		return []byte(f.logs), nil
	}
	return nil, nil
}

func (f *fakeRun) saw(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func fakeEnv(f *fakeRun, exists ...string) DevEnv {
	set := map[string]bool{}
	for _, e := range exists {
		set[e] = true
	}
	return DevEnv{
		Home:   "/h",
		Exists: func(p string) bool { return set[p] },
		Glob:   func(string) []string { return nil },
		Run:    f.run,
		LookPath: func(n string) (string, error) {
			if n == "docker" || n == "devpod" {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
	}
}

var testFP = "SHA256:" + strings.Repeat("A", 43)

func TestDetectRuntime(t *testing.T) {
	f := &fakeRun{}
	rt, err := DetectRuntime(context.Background(), fakeEnv(f, "/h/.colima/default/docker.sock"))
	if err != nil || rt.Name != RuntimeColima || rt.DockerHost() != "unix:///h/.colima/default/docker.sock" {
		t.Fatalf("%+v %v", rt, err)
	}
	rt, err = DetectRuntime(context.Background(), fakeEnv(f, "/h/.colima/default/docker.sock", "/h/.orbstack/run/docker.sock"))
	if err != nil || rt.Name != RuntimeOrbStack {
		t.Fatalf("orbstack first: %+v %v", rt, err)
	}
}

func TestDetectRuntimeRefusals(t *testing.T) {
	f := &fakeRun{}
	_, err := DetectRuntime(context.Background(), fakeEnv(f))
	if DevErrorCode(err) != DevErrNoRuntime {
		t.Fatalf("%v", err)
	}
	_, err = DetectRuntime(context.Background(), fakeEnv(f, "/h/.docker/run/docker.sock"))
	if DevErrorCode(err) != DevErrDockerDesktop {
		t.Fatalf("Docker Desktop must be refused: %v", err)
	}
	env := fakeEnv(f)
	env.LookPath = func(string) (string, error) { return "", errors.New("none") }
	_, err = DetectRuntime(context.Background(), env)
	if DevErrorCode(err) != DevErrNoRuntime {
		t.Fatalf("no docker cli: %v", err)
	}
}

func TestDevcontainerFile(t *testing.T) {
	s, err := DevcontainerFile(Devcontainer{Template: "go"}, Size{CPUs: 2, MemoryMB: 1024})
	if err != nil || !strings.Contains(s, "devcontainers/go") || !strings.Contains(s, "--cpus=2") || !strings.Contains(s, "--memory=1024m") {
		t.Fatalf("%s %v", s, err)
	}
	s, err = DevcontainerFile(Devcontainer{JSON: `{"image":"x","runArgs":["--init"]}`}, Size{CPUs: 1})
	if err != nil || !strings.Contains(s, "--init") || !strings.Contains(s, "--cpus=1") {
		t.Fatalf("%s %v", s, err)
	}
	if s, err = DevcontainerFile(Devcontainer{RepoURL: "https://x/y"}, Size{CPUs: 1}); s != "" || err != nil {
		t.Fatal("repo source brings its own file")
	}
	if _, err = DevcontainerFile(Devcontainer{Template: "cobol"}, Size{}); DevErrorCode(err) != DevErrInvalid {
		t.Fatalf("%v", err)
	}
}

func TestDevPodArgsAndWorkspaceID(t *testing.T) {
	a := strings.Join(DevPodUpArgs("/src", "ws1", true), " ")
	if a != "up /src --id ws1 --provider docker --ide none --recreate" {
		t.Fatal(a)
	}
	if id := DevWorkspaceID("AB_c-9"); id != "nexal-ab-c-9" || !ValidID(id) {
		t.Fatal(id)
	}
	s := strings.Join(SidecarRunArgs("ws1", "cid", "img:1", "/e.env", true), " ")
	for _, want := range []string{"--network container:cid", "--cap-add NET_ADMIN", "--device /dev/net/tun", "--env-file /e.env",
		"-v nexal-mesh-ws1:/var/lib/nexal", "img:1"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(strings.Join(SidecarRunArgs("ws1", "cid", "img:1", "/e.env", false), " "), "-v ") {
		t.Fatal("ephemeral sidecar keeps no identity volume")
	}
}

func upSpec() DevUpSpec {
	return DevUpSpec{Workspace: "nexal-sb1", Hostname: "sbx-1", Devcontainer: Devcontainer{Template: "go"},
		Size: Size{CPUs: 2, MemoryMB: 2048, DiskGB: 10}, SetupKey: "KEY-1", Lifecycle: LifecycleEphemeral, Timeout: time.Second}
}

func newTestDevPod(f *fakeRun, dir string, image string) *DevPod {
	return NewDevPod(DevConfig{Env: fakeEnv(f, "/h/.colima/default/docker.sock"), StateDir: dir, MeshImage: image,
		Sleep: func(time.Duration) {}})
}

func TestDevPodUpJoinsMeshAndCleansSecrets(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{logs: "boot...\nNEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\",\"hostKeyFingerprint\":\"" + testFP + "\"}\n"}
	d := newTestDevPod(f, dir, "mesh:1")
	res, err := d.Up(context.Background(), upSpec())
	if err != nil || res.MeshIP != "100.64.1.9" || res.HostKeyFingerprint != testFP {
		t.Fatalf("%+v %v", res, err)
	}
	if !f.saw("devpod up ") || !f.saw("--provider docker") || !f.saw("--network container:cid123") {
		t.Fatalf("commands: %v", f.cmds)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "KEY-1") {
			t.Fatalf("setup key leaked on a command line: %s", c)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "nexal-sb1", "mesh.env")); !os.IsNotExist(err) {
		t.Fatal("mesh.env (secrets) must be removed after join")
	}
}

func TestDevPodUpRefusals(t *testing.T) {
	dir := t.TempDir()
	if _, err := newTestDevPod(&fakeRun{}, dir, "").Up(context.Background(), upSpec()); DevErrorCode(err) != DevErrUnconfigured {
		t.Fatalf("%v", err)
	}
	// No runtime at all: refused with the clear code.
	d := NewDevPod(DevConfig{Env: fakeEnv(&fakeRun{}), StateDir: dir, MeshImage: "m", Sleep: func(time.Duration) {}})
	if _, err := d.Up(context.Background(), upSpec()); DevErrorCode(err) != DevErrNoRuntime {
		t.Fatalf("%v", err)
	}
	bad := upSpec()
	bad.Hostname = "Bad Host"
	if _, err := newTestDevPod(&fakeRun{}, dir, "m").Up(context.Background(), bad); DevErrorCode(err) != DevErrInvalid {
		t.Fatalf("%v", err)
	}
}

func TestDevPodUpFailureCleansUpFreshWorkspace(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{devpodUp: errors.New("exit status 1")}
	_, err := newTestDevPod(f, dir, "m").Up(context.Background(), upSpec())
	if DevErrorCode(err) != DevErrDevPodFailed {
		t.Fatalf("%v", err)
	}
	if !f.saw("volume rm -f nexal-mesh-nexal-sb1") {
		t.Fatalf("failed fresh workspace must be cleaned up: %v", f.cmds)
	}
	if _, err := os.Stat(filepath.Join(dir, "nexal-sb1")); !os.IsNotExist(err) {
		t.Fatal("state dir must be gone")
	}
}

func TestDevPodDeleteRemovesEverything(t *testing.T) {
	dir := t.TempDir()
	ws := "nexal-sb1"
	if err := os.MkdirAll(filepath.Join(dir, ws, "devpod"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ws, "mesh.env"), []byte("NEXAL_SETUP_KEY=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRun{}
	if err := newTestDevPod(f, dir, "m").Delete(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rm -f -v nexal-mesh-nexal-sb1", "devpod delete nexal-sb1 --force", "ps -aq --filter label=dev.containers.id=nexal-sb1",
		"rm -f -v cid123", "volume rm -f nexal-mesh-nexal-sb1"} {
		if !f.saw(want) {
			t.Errorf("missing command %q in %v", want, f.cmds)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ws)); !os.IsNotExist(err) {
		t.Fatal("secrets and DevPod state must be removed")
	}
	if err := newTestDevPod(f, dir, "m").Delete(context.Background(), ws); err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
	if newTestDevPod(f, dir, "m").Delete(context.Background(), "../x") == nil {
		t.Fatal("invalid workspace id")
	}
}

func TestIgnoreMissing(t *testing.T) {
	if ignoreMissing(errors.New("exit 1: Error: No such container: x")) != nil {
		t.Fatal("missing is fine")
	}
	if ignoreMissing(errors.New("permission denied")) == nil {
		t.Fatal("other errors stay")
	}
}
