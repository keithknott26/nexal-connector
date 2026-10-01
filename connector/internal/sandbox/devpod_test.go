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
	labels   string // docker inspect .Config.Labels
	sshd     string // output of the DevSSHDScript exec
	bootSeen string // join file content while the sidecar starts
	// noNetwork makes `docker network inspect` fail (the tenant network does not exist yet).
	noNetwork bool
}

func (f *fakeRun) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.cmds = append(f.cmds, line)
	f.mu.Unlock()
	switch {
	case strings.Contains(line, "network inspect") && f.noNetwork:
		return []byte("Error: No such network"), errors.New("exit status 1")
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
	case strings.Contains(line, " inspect --format"):
		return []byte(f.labels), nil
	case strings.Contains(line, " exec -u 0 "):
		return []byte(f.sshd), nil
	case strings.Contains(line, " run -d --name nexal-mesh-"):
		for i, a := range args {
			if a == "--mount" && i+1 < len(args) {
				src := strings.TrimPrefix(strings.Split(args[i+1], ",")[1], "src=")
				b, _ := os.ReadFile(filepath.Join(src, SidecarBootFile))
				f.mu.Lock()
				f.bootSeen = string(b)
				f.mu.Unlock()
			}
		}
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
	// Colima installed but stopped, Docker Desktop also present: say Colima isn't running.
	_, err = DetectRuntime(context.Background(), fakeEnv(f, "/h/.docker/run/docker.sock", "/opt/homebrew/bin/colima"))
	if DevErrorCode(err) != DevErrNoRuntime || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("stopped Colima: %v", err)
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
	s, err = DevcontainerFile(Devcontainer{Template: DevboxTemplate}, Size{CPUs: 1, MemoryMB: 512, DiskGB: 4})
	if err != nil || !strings.Contains(s, DefaultDevboxImage) || !strings.Contains(s, "--memory-swap=512m") || !strings.Contains(s, "--pids-limit=") {
		t.Fatalf("devbox template: %s %v", s, err)
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
	s := strings.Join(SidecarRunArgs("ws1", "cid", "img:1", "/st/ws1/boot", true), " ")
	for _, want := range []string{"--network container:cid", "--cap-add NET_ADMIN", "--device /dev/net/tun",
		"--mount type=bind,src=/st/ws1/boot,dst=/run/nexal-boot", "-v nexal-mesh-ws1:/var/lib/nexal", "img:1",
		"--cpus=0.5", "--memory=128m", "--memory-swap=128m", "--pids-limit=256"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	for _, bad := range []string{"--env-file", "-e ", "--privileged", "SYS_ADMIN", "/dev/fuse"} {
		if strings.Contains(s, bad) {
			t.Errorf("sidecar must not get %q: %s", bad, s)
		}
	}
	if strings.Contains(strings.Join(SidecarRunArgs("ws1", "cid", "img:1", "/e", false), " "), "-v ") {
		t.Fatal("ephemeral sidecar keeps no identity volume")
	}
}

func upSpec() DevUpSpec {
	return DevUpSpec{Workspace: "nexal-sb1", Hostname: "sbx-1", Devcontainer: Devcontainer{Template: "go"},
		Size: Size{CPUs: 2, MemoryMB: 2048, DiskGB: 10}, SetupKey: "KEY-1", ManagementURL: "https://mesh.example.com",
		Lifecycle: LifecycleEphemeral, Timeout: time.Second}
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
	// Without an SSH CA there is no sshd, so no host key (the sidecar's own report
	// carries none; a stray fingerprint there is ignored).
	if err != nil || res.MeshIP != "100.64.1.9" || res.HostKeyFingerprint != "" || res.DriveUnavailable != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if !f.saw("devpod up ") || !f.saw("--provider docker") || !f.saw("--network container:cid123") ||
		!f.saw("docker update --cpus=2 --memory=2048m --memory-swap=2048m --pids-limit=4096 cid123") {
		t.Fatalf("commands: %v", f.cmds)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "KEY-1") {
			t.Fatalf("setup key leaked on a command line: %s", c)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "nexal-sb1", "boot")); !os.IsNotExist(err) {
		t.Fatal("the boot dir (secrets) must be removed after join")
	}
	for _, want := range []string{"NEXAL_SETUP_KEY=KEY-1\n", "NEXAL_MESH_URL=https://mesh.example.com\n", "NEXAL_HOSTNAME=sbx-1\n"} {
		if !strings.Contains(f.bootSeen, want) {
			t.Errorf("join file lacks %q: %q", want, f.bootSeen)
		}
	}
	if strings.Contains(f.bootSeen, "NEXAL_MANAGEMENT_URL") {
		t.Error("the join file uses the VM name NEXAL_MESH_URL")
	}
	if f.saw(" exec ") {
		t.Error("no ssh setup without an SSH CA")
	}
}

const testCA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBsdX0q6S3yI6lkbDfH5d4gUlPu8uNCw4j3QhW1bM6a5 nexal-ca"
const devTestHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID83UXuqzoFyi/uB9YPyWVuvR+SA38DI5HgNO2ELOEd9"
const devTestHostFP = "SHA256:Q0vUGy+EIBre481MT2ggo+D10IQ7qcimVizOqILqOiQ"

func TestDevPodUpStartsSSHInTheDevContainer(t *testing.T) {
	f := &fakeRun{logs: "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n",
		labels: `{"devcontainer.metadata":"[{\"remoteUser\":\"root\"},{\"remoteUser\":\"vscode\"}]"}`,
		sshd:   "noise\nNEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\",\"hostKeyFingerprint\":\"" + devTestHostFP + "\",\"hostKey\":\"" + devTestHostKey + "\"}\n"}
	s := upSpec()
	s.SSHCAPublicKey, s.DriveToken, s.DriveMode = testCA, "drive-token-123456", "ro"
	res, err := newTestDevPod(f, t.TempDir(), "mesh:1").Up(context.Background(), s)
	if err != nil || res.MeshIP != "100.64.1.9" || res.HostKey != devTestHostKey || res.HostKeyFingerprint != devTestHostFP {
		t.Fatalf("%+v %v", res, err)
	}
	if res.DriveUnavailable != DevDriveUnavailable {
		t.Fatalf("drive requested but not available must be explicit: %+v", res)
	}
	if !f.saw("exec -u 0 cid123 sh -c ") || !f.saw(" nexal-sshd 100.64.1.9 vscode "+testCA) {
		t.Fatalf("sshd must be set up in the dev container: %v", f.cmds)
	}
	if strings.Contains(f.bootSeen, "drive-token") || strings.Contains(f.bootSeen, "NEXAL_SSH_CA") {
		t.Fatalf("the sidecar gets neither the drive token nor the CA: %q", f.bootSeen)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "drive-token") {
			t.Fatalf("drive token on a command line: %s", c)
		}
	}
}

func TestDevPodUpSSHFailures(t *testing.T) {
	for name, out := range map[string]string{
		"reported": "NEXAL-FIRSTBOOT-FAILED no sshd in the dev container\n",
		"silent":   "",
		"other ip": "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.10\",\"hostKey\":\"" + devTestHostKey + "\"}\n",
		"no key":   "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n",
	} {
		dir := t.TempDir()
		f := &fakeRun{logs: "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n", sshd: out}
		s := upSpec()
		s.SSHCAPublicKey = testCA
		if _, err := newTestDevPod(f, dir, "m").Up(context.Background(), s); DevErrorCode(err) != DevErrSSHFailed {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "nexal-sb1")); !os.IsNotExist(err) {
			t.Errorf("%s: a failed fresh workspace is removed", name)
		}
	}
}

func TestRemoteUserFromLabels(t *testing.T) {
	for in, want := range map[string]string{
		`{"devcontainer.metadata":"[{\"remoteUser\":\"node\"}]"}`:                         "node",
		`{"devcontainer.metadata":"[{\"containerUser\":\"dev\"},{\"remoteUser\":\"\"}]"}`: "dev",
		`{"devcontainer.metadata":"[{\"remoteUser\":\"x; rm -rf /\"}]"}`:                  "",
		`{"other":"1"}`: "",
		`null`:          "",
		``:              "",
	} {
		if got := RemoteUserFromLabels(in); got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
}

func TestDevPodUpNeedsManagementURL(t *testing.T) {
	s := upSpec()
	s.ManagementURL = ""
	if _, err := newTestDevPod(&fakeRun{}, t.TempDir(), "m").Up(context.Background(), s); DevErrorCode(err) != DevErrInvalid {
		t.Fatalf("%v", err)
	}
	s.ManagementURL = "http://plain.example"
	if _, err := newTestDevPod(&fakeRun{}, t.TempDir(), "m").Up(context.Background(), s); DevErrorCode(err) != DevErrInvalid {
		t.Fatalf("%v", err)
	}
}

func TestSidecarEntrypointMatchesConnector(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "sidecar", "entrypoint.sh"))
	if err != nil {
		t.Skip(err)
	}
	ep := string(b)
	for _, line := range strings.Split(strings.TrimSpace(SidecarEnv(upSpec())), "\n") {
		if k := strings.SplitN(line, "=", 2)[0]; !strings.Contains(ep, k) {
			t.Errorf("entrypoint does not read %s", k)
		}
	}
	if !strings.Contains(ep, SidecarBootMount+"/"+SidecarBootFile) {
		t.Errorf("entrypoint does not read %s/%s", SidecarBootMount, SidecarBootFile)
	}
	if strings.Contains(ep, "/usr/sbin/sshd") || strings.Contains(ep, "sshd -D") {
		t.Error("the sidecar must not run sshd")
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
	if err := os.MkdirAll(filepath.Join(dir, ws, "boot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ws, "boot", "mesh.env"), []byte("NEXAL_SETUP_KEY=x"), 0o600); err != nil {
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

func TestResolveMeshImage(t *testing.T) {
	if DefaultMeshImage == "" || !strings.Contains(DefaultMeshImage, "nexal-mesh-sidecar:") || strings.HasSuffix(DefaultMeshImage, ":latest") {
		t.Fatalf("default must be a version-pinned tag: %q", DefaultMeshImage)
	}
	for _, empty := range []string{"", "  ", "\n"} {
		if got := ResolveMeshImage(empty); got != DefaultMeshImage {
			t.Fatalf("%q -> %q, want default", empty, got)
		}
	}
	if got := ResolveMeshImage(" registry.local/mesh:dev \n"); got != "registry.local/mesh:dev" {
		t.Fatalf("override not used: %q", got)
	}
	// With the default, Up gets past the unconfigured refusal (and then stops at the fake runtime).
	d := NewDevPod(DevConfig{Env: fakeEnv(&fakeRun{}), StateDir: t.TempDir(), MeshImage: ResolveMeshImage(""), Sleep: func(time.Duration) {}})
	if _, err := d.Up(context.Background(), upSpec()); DevErrorCode(err) == DevErrUnconfigured {
		t.Fatalf("default image must configure the backend: %v", err)
	}
}

func TestDevLimitArgs(t *testing.T) {
	if got := strings.Join(DevLimitArgs(Size{CPUs: 1, MemoryMB: 512, DiskGB: 4}), " "); got != "--cpus=1 --memory=512m --memory-swap=512m --pids-limit=4096" {
		t.Fatal(got)
	}
	if len(DevLimitArgs(Size{})) != 0 {
		t.Fatal("no size, no limits")
	}
	for _, a := range DevLimitArgs(Size{CPUs: 4, MemoryMB: 2048, DiskGB: 16}) {
		if strings.Contains(a, "storage-opt") {
			t.Fatal("disk is not enforceable on the Mac container VMs")
		}
	}
}

func TestDevPodUpLimitsRepoSources(t *testing.T) {
	f := &fakeRun{logs: "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n"}
	s := upSpec()
	s.Devcontainer = Devcontainer{RepoURL: "https://github.com/x/y"}
	s.Size = Size{CPUs: 1, MemoryMB: 512, DiskGB: 4}
	if _, err := newTestDevPod(f, t.TempDir(), "m").Up(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !f.saw("docker update --cpus=1 --memory=512m --memory-swap=512m") {
		t.Fatalf("repo sources get limits via docker update: %v", f.cmds)
	}
}

func TestWorkspaceBuiltImages(t *testing.T) {
	got := WorkspaceBuiltImages("devpod-abc123:latest\nvsc-ws-uid\n" + DefaultDevboxImage + "\ndevpod-abc123:latest\nmcr.microsoft.com/devcontainers/go\n")
	if strings.Join(got, ",") != "devpod-abc123:latest,vsc-ws-uid" {
		t.Fatal(got)
	}
}

func TestDevPodDeleteRemovesBuiltImages(t *testing.T) {
	f := &fakeRun{labels: "devpod-1f2e:latest\n"}
	if err := newTestDevPod(f, t.TempDir(), "m").Delete(context.Background(), "nexal-sb1"); err != nil {
		t.Fatal(err)
	}
	if !f.saw("inspect --format {{.Config.Image}} cid123") || !f.saw("image rm devpod-1f2e:latest") {
		t.Fatalf("built image must be removed: %v", f.cmds)
	}
}

func TestDevPathPutsDockerFirst(t *testing.T) {
	got := devPath(DevEnv{Home: "/h"}, "/opt/homebrew/bin/docker")
	if !strings.HasPrefix(got, "/opt/homebrew/bin:") || strings.Count(got, "/opt/homebrew/bin") != 1 || !strings.Contains(got, ":/usr/bin:") {
		t.Fatalf("%s", got)
	}
}
