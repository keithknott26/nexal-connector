package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Dev containers (kind=devcontainer).
//
// DevPod (github.com/loft-sh/devpod, MPL-2.0) is used only as an UNMODIFIED
// external program: this file shells out to the `devpod` binary and never links,
// vendors or patches it (docs/THROWAWAY-HOSTS.md §11 license handling). The
// container runtime must be Colima, Lima, Podman or OrbStack; Docker Desktop is
// refused because of its licence. The runner keeps DevPod's own state in a
// private DEVPOD_HOME so it never touches the owner's own DevPod setup and
// teardown can remove everything.
//
// Mesh join: after `devpod up`, a sidecar container (DevConfig.MeshImage, the
// neXal mesh runtime with the first-boot entrypoint) is started in the dev
// container's network namespace (--network container:<id>, NET_ADMIN, /dev/net/tun).
// It joins the mesh with the one-use key, prints the same
// `NEXAL-FIRSTBOOT {...}` line as a VM on its log, and runs the SSH/drive pieces.

// DevOps is the dev-container backend the Manager drives. Fakes implement it in tests.
type DevOps interface {
	// Up creates (or, for a persistent workspace with Recreate false, restarts) the
	// workspace and joins the mesh. It returns once the guest reports its mesh IP.
	Up(ctx context.Context, s DevUpSpec) (DevUpResult, error)
	// Alive reports whether the workspace container and its mesh sidecar run.
	Alive(ctx context.Context, workspace string) (bool, error)
	// Delete removes everything: container, volumes, mesh sidecar, DevPod state,
	// secrets. It is idempotent. (The coordinator removes the mesh peer.)
	Delete(ctx context.Context, workspace string) error
}

// DevUpSpec is one Up request. It carries secrets (SetupKey, DriveToken).
type DevUpSpec struct {
	Workspace      string
	Hostname       string
	Devcontainer   Devcontainer
	Size           Size
	SetupKey       string
	ManagementURL  string
	SSHCAPublicKey string
	DriveMode      string
	DriveToken     string
	Lifecycle      Lifecycle
	// Recreate wipes any existing workspace first (ephemeral lifecycle).
	Recreate bool
	Timeout  time.Duration // mesh join wait; default 3 min
}

// String implements fmt.Stringer without any secret.
func (s DevUpSpec) String() string { return "sandbox.DevUpSpec{workspace=" + s.Workspace + "}" }

// DevUpResult is what a successful Up reports.
type DevUpResult struct {
	MeshIP             string
	HostKeyFingerprint string
	HostKey            string
}

// Error codes for DevError.
const (
	DevErrNoRuntime     = "no_container_runtime"
	DevErrDockerDesktop = "docker_desktop_only"
	DevErrDevPodMissing = "devpod_missing"
	DevErrDevPodFailed  = "devpod_failed"
	DevErrMeshFailed    = "mesh_sidecar_failed"
	DevErrUnconfigured  = "mesh_image_unconfigured"
	DevErrInvalid       = "invalid_devcontainer"
	DevErrNotAvailable  = "devcontainer_unavailable"
)

// DevError is a refusal or failure with a stable machine-readable code. Its
// message is safe to show to the user and never contains a secret.
type DevError struct {
	Code string
	Msg  string
}

func (e *DevError) Error() string { return e.Code + ": " + e.Msg }

func devErr(code, format string, args ...any) error {
	return &DevError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// DevErrorCode returns err's DevError code, or "".
func DevErrorCode(err error) string {
	var de *DevError
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// RuntimeName is a supported container runtime.
type RuntimeName string

const (
	RuntimeOrbStack RuntimeName = "orbstack"
	RuntimeColima   RuntimeName = "colima"
	RuntimeLima     RuntimeName = "lima"
	RuntimePodman   RuntimeName = "podman"
)

// RuntimeInfo is a detected, working runtime.
type RuntimeInfo struct {
	Name   RuntimeName
	Socket string
}

// DockerHost is the DOCKER_HOST value that selects this runtime.
func (r RuntimeInfo) DockerHost() string { return "unix://" + r.Socket }

// DevEnv is the host access DetectRuntime needs; fakes replace it in tests.
type DevEnv struct {
	Home string
	// Exists reports whether a path exists.
	Exists func(path string) bool
	// Glob lists paths matching a pattern.
	Glob func(pattern string) []string
	// Run runs a command with extra environment ("K=V") and returns combined output.
	Run func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	// LookPath finds an executable on PATH.
	LookPath func(name string) (string, error)
}

// SystemDevEnv is the real host.
func SystemDevEnv() DevEnv {
	home, _ := os.UserHomeDir()
	return DevEnv{
		Home:   home,
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		Glob:   func(p string) []string { m, _ := filepath.Glob(p); return m },
		Run: func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = append(os.Environ(), env...)
			return cmd.CombinedOutput()
		},
		LookPath: exec.LookPath,
	}
}

// FindBinary looks for name on PATH, then in the usual Homebrew locations (a
// launchd-started connector has a minimal PATH).
func (e DevEnv) FindBinary(name string) (string, bool) {
	if p, err := e.LookPath(name); err == nil && p != "" {
		return p, true
	}
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(e.Home, ".local", "bin")} {
		p := filepath.Join(d, name)
		if e.Exists(p) {
			return p, true
		}
	}
	return "", false
}

// runtimeCandidate is a socket a supported runtime exposes.
type runtimeCandidate struct {
	name   RuntimeName
	socket string
}

// RuntimeCandidates lists the sockets of the supported runtimes in preference
// order. Podman's socket is asked from `podman` itself (see DetectRuntime).
func RuntimeCandidates(e DevEnv) []runtimeCandidate {
	var c []runtimeCandidate
	c = append(c, runtimeCandidate{RuntimeOrbStack, filepath.Join(e.Home, ".orbstack", "run", "docker.sock")})
	c = append(c, runtimeCandidate{RuntimeColima, filepath.Join(e.Home, ".colima", "default", "docker.sock")})
	for _, m := range e.Glob(filepath.Join(e.Home, ".colima", "*", "docker.sock")) {
		c = append(c, runtimeCandidate{RuntimeColima, m})
	}
	c = append(c, runtimeCandidate{RuntimeLima, filepath.Join(e.Home, ".lima", "docker", "sock", "docker.sock")})
	return c
}

// DetectRuntime finds a usable, licence-compatible container runtime. It returns
// DevErrNoRuntime when none is running, or DevErrDockerDesktop when only Docker
// Desktop is present. A candidate counts only if the docker CLI can talk to it.
func DetectRuntime(ctx context.Context, e DevEnv) (RuntimeInfo, error) {
	docker, ok := e.FindBinary("docker")
	if !ok {
		return RuntimeInfo{}, devErr(DevErrNoRuntime, "the docker command-line client was not found; install Colima, Lima, Podman or OrbStack")
	}
	works := func(sock string) bool {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		_, err := e.Run(cctx, nil, docker, "--host", "unix://"+sock, "version", "--format", "{{.Server.Version}}")
		return err == nil
	}
	for _, c := range RuntimeCandidates(e) {
		if e.Exists(c.socket) && works(c.socket) {
			return RuntimeInfo{Name: c.name, Socket: c.socket}, nil
		}
	}
	if podman, ok := e.FindBinary("podman"); ok {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		out, err := e.Run(cctx, nil, podman, "info", "--format", "{{.Host.RemoteSocket.Path}}")
		cancel()
		if sock := strings.TrimSpace(string(out)); err == nil && strings.HasPrefix(sock, "/") && e.Exists(sock) && works(sock) {
			return RuntimeInfo{Name: RuntimePodman, Socket: sock}, nil
		}
	}
	if e.Exists(filepath.Join(e.Home, ".docker", "run", "docker.sock")) || e.Exists("/Applications/Docker.app") {
		return RuntimeInfo{}, devErr(DevErrDockerDesktop, "only Docker Desktop was found, which is not supported; start Colima, Lima, Podman or OrbStack")
	}
	return RuntimeInfo{}, devErr(DevErrNoRuntime, "no running Colima, Lima, Podman or OrbStack was found")
}

// DevWorkspaceID derives a DevPod workspace id (lowercase, digits, dashes) from a
// sandbox id.
func DevWorkspaceID(sandboxID string) string {
	var b strings.Builder
	b.WriteString("nexal-")
	for _, r := range strings.ToLower(sandboxID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	return s
}

// workspaceLabel is the container label DevPod puts on a workspace's container.
// Verify during the DevPod spike; everything that finds the container goes
// through it.
const workspaceLabel = "dev.containers.id"

// templateImages maps the catalog templates to dev container images.
var templateImages = map[string]string{
	"ubuntu": "mcr.microsoft.com/devcontainers/base:ubuntu",
	"debian": "mcr.microsoft.com/devcontainers/base:debian",
	"node":   "mcr.microsoft.com/devcontainers/typescript-node",
	"python": "mcr.microsoft.com/devcontainers/python",
	"go":     "mcr.microsoft.com/devcontainers/go",
	"rust":   "mcr.microsoft.com/devcontainers/rust",
}

// DevcontainerFile renders the devcontainer.json for a template or inline
// payload, with the Mac owner's resource limits added as runArgs. Repo sources
// bring their own file and return "" (DevPod reads it from the checkout).
func DevcontainerFile(d Devcontainer, size Size) (string, error) {
	var m map[string]any
	switch {
	case d.Template != "":
		img, ok := templateImages[d.Template]
		if !ok {
			return "", devErr(DevErrInvalid, "unknown template %q", d.Template)
		}
		m = map[string]any{"name": "nexal-" + d.Template, "image": img}
	case d.JSON != "":
		if err := json.Unmarshal([]byte(d.JSON), &m); err != nil || m == nil {
			return "", devErr(DevErrInvalid, "devcontainer json must be an object")
		}
	default:
		return "", nil
	}
	var args []any
	if old, ok := m["runArgs"].([]any); ok {
		args = old
	}
	if size.CPUs > 0 {
		args = append(args, fmt.Sprintf("--cpus=%d", size.CPUs))
	}
	if size.MemoryMB > 0 {
		args = append(args, fmt.Sprintf("--memory=%dm", size.MemoryMB))
	}
	if len(args) > 0 {
		m["runArgs"] = args
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", devErr(DevErrInvalid, "cannot render devcontainer.json")
	}
	return string(b) + "\n", nil
}

// DevPodUpArgs builds the `devpod up` arguments. It is pure.
func DevPodUpArgs(source, workspace string, recreate bool) []string {
	a := []string{"up", source, "--id", workspace, "--provider", "docker", "--ide", "none"}
	if recreate {
		a = append(a, "--recreate")
	}
	return a
}

// SidecarName and SidecarVolume name the mesh sidecar's container and its
// identity volume (persistent workspaces only).
func SidecarName(workspace string) string   { return "nexal-mesh-" + workspace }
func SidecarVolume(workspace string) string { return "nexal-mesh-" + workspace }

// SidecarRunArgs builds the `docker run` arguments for the mesh sidecar. The
// secrets are in envFile, not on the command line. It is pure.
func SidecarRunArgs(workspace, containerID, image, envFile string, persistent bool) []string {
	a := []string{"run", "-d", "--name", SidecarName(workspace),
		"--label", "nexal.workspace=" + workspace,
		"--network", "container:" + containerID,
		"--cap-add", "NET_ADMIN", "--device", "/dev/net/tun",
		"--env-file", envFile}
	if persistent {
		a = append(a, "-v", SidecarVolume(workspace)+":/var/lib/nexal")
	}
	return append(a, image)
}

// SidecarEnv renders the sidecar's env file (KEY=VALUE lines). Values were
// validated by ValidateBoot; the file is written 0600 and deleted after join.
func SidecarEnv(s DevUpSpec) string {
	var b strings.Builder
	w := func(k, v string) { b.WriteString(k + "=" + v + "\n") }
	w("NEXAL_HOSTNAME", s.Hostname)
	w("NEXAL_SETUP_KEY", s.SetupKey)
	lc := s.Lifecycle
	if lc == "" {
		lc = LifecycleEphemeral
	}
	w("NEXAL_LIFECYCLE", string(lc))
	if s.ManagementURL != "" {
		w("NEXAL_MANAGEMENT_URL", s.ManagementURL)
	}
	if s.SSHCAPublicKey != "" {
		w("NEXAL_SSH_CA", s.SSHCAPublicKey)
	}
	if s.DriveMode != "" {
		w("NEXAL_DRIVE_MODE", s.DriveMode)
	}
	if s.DriveToken != "" {
		w("NEXAL_DRIVE_TOKEN", s.DriveToken)
	}
	return b.String()
}

// MeshImageEnv overrides DefaultMeshImage (a local build, a registry mirror, a
// newer sidecar). An empty or unset value means the default.
const MeshImageEnv = "NEXAL_DEV_MESH_IMAGE"

// DefaultMeshImage is the version-pinned mesh sidecar image the connector uses
// when MeshImageEnv is not set. It is built from connector/sidecar by
// .github/workflows/sidecar-image.yml on a `sidecar-v<version>` tag, which
// pushes ghcr.io/<owner>/nexal-mesh-sidecar:<version>. The package must be
// public so a Mac can pull it anonymously. Bump the tag here whenever a new
// sidecar version is released.
const DefaultMeshImage = "ghcr.io/keithknott26/nexal-mesh-sidecar:0.1.0"

// ResolveMeshImage returns override (trimmed) or, when empty, DefaultMeshImage.
func ResolveMeshImage(override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	return DefaultMeshImage
}

// DevConfig configures the DevPod backend.
type DevConfig struct {
	Env       DevEnv
	StateDir  string // per-workspace directories live here (0700)
	MeshImage string // sidecar image with the neXal mesh runtime
	Sleep     func(time.Duration)
	Now       func() time.Time
}

// DevPod is the DevOps implementation that shells out to devpod and docker.
type DevPod struct {
	cfg DevConfig
	mu  sync.Mutex // one workspace operation at a time keeps DEVPOD_HOME sane
}

// NewDevPod builds the backend. MeshImage may be empty; Up then refuses with
// DevErrUnconfigured. The connector wiring passes ResolveMeshImage(env), so it
// is never empty there.
func NewDevPod(cfg DevConfig) *DevPod {
	if cfg.Env.Run == nil {
		cfg.Env = SystemDevEnv()
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &DevPod{cfg: cfg}
}

// DefaultDevStateDir is ~/Library/Application Support/Nexal/devcontainers.
func DefaultDevStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Nexal", "devcontainers"), nil
}

func (d *DevPod) wsDir(ws string) string { return filepath.Join(d.cfg.StateDir, ws) }

type devTools struct {
	rt     RuntimeInfo
	docker string
	devpod string
	env    []string
}

func (d *DevPod) tools(ctx context.Context, ws string, needDevPod bool) (devTools, error) {
	rt, err := DetectRuntime(ctx, d.cfg.Env)
	if err != nil {
		return devTools{}, err
	}
	docker, _ := d.cfg.Env.FindBinary("docker")
	t := devTools{rt: rt, docker: docker}
	t.env = []string{"DOCKER_HOST=" + rt.DockerHost(), "DEVPOD_HOME=" + filepath.Join(d.wsDir(ws), "devpod")}
	if needDevPod {
		p, ok := d.cfg.Env.FindBinary("devpod")
		if !ok {
			return devTools{}, devErr(DevErrDevPodMissing, "the devpod command-line tool is not installed")
		}
		t.devpod = p
	}
	return t, nil
}

func (d *DevPod) run(ctx context.Context, t devTools, bin string, args ...string) (string, error) {
	out, err := d.cfg.Env.Run(ctx, t.env, bin, args...)
	if err != nil {
		// Keep the tool's output in the error so ignoreMissing can see "No such ...".
		err = errors.New(err.Error() + ": " + trimOutput(out))
	}
	return string(out), err
}

// containerID finds the workspace container id ("" if none).
func (d *DevPod) containerID(ctx context.Context, t devTools, ws string, runningOnly bool) (string, error) {
	args := []string{"ps", "-q", "--filter", "label=" + workspaceLabel + "=" + ws}
	if runningOnly {
		args = append(args, "--filter", "status=running")
	}
	out, err := d.run(ctx, t, t.docker, args...)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l, nil
		}
	}
	return "", nil
}

// Up implements DevOps.
func (d *DevPod) Up(ctx context.Context, s DevUpSpec) (res DevUpResult, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfg.MeshImage == "" {
		return res, devErr(DevErrUnconfigured, "no mesh sidecar image is configured (the connector defaults to its pinned release image; %s overrides it)", MeshImageEnv)
	}
	if !ValidID(s.Workspace) || !ValidHostname(s.Hostname) || !validSecret(s.SetupKey) {
		return res, devErr(DevErrInvalid, "invalid dev container request")
	}
	if err := ValidateDevcontainer(&s.Devcontainer); err != nil {
		return res, devErr(DevErrInvalid, "%v", err)
	}
	file, err := DevcontainerFile(s.Devcontainer, s.Size)
	if err != nil {
		return res, err
	}
	t, err := d.tools(ctx, s.Workspace, true)
	if err != nil {
		return res, err
	}
	if s.Recreate {
		if err := d.deleteLocked(ctx, s.Workspace); err != nil {
			return res, err
		}
	}
	// Anything that goes wrong below leaves nothing behind for a NEW workspace;
	// a kept (persistent) workspace is never wiped by a failed restart.
	fresh := s.Recreate || s.Lifecycle != LifecyclePersistent
	defer func() {
		if err != nil && fresh {
			_ = d.deleteLocked(context.WithoutCancel(ctx), s.Workspace)
		}
	}()

	dir := d.wsDir(s.Workspace)
	if err = os.MkdirAll(filepath.Join(dir, "devpod"), 0o700); err != nil {
		return res, err
	}
	source := s.Devcontainer.RepoURL
	if file != "" {
		src := filepath.Join(dir, "src")
		if err = os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o700); err != nil {
			return res, err
		}
		if err = os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte(file), 0o600); err != nil {
			return res, err
		}
		source = src
	}
	// Ensure the docker provider exists in this private DEVPOD_HOME; "already
	// exists" is fine.
	_, _ = d.run(ctx, t, t.devpod, "provider", "add", "docker")
	_, _ = d.run(ctx, t, t.devpod, "provider", "use", "docker")
	if out, uerr := d.run(ctx, t, t.devpod, DevPodUpArgs(source, s.Workspace, false)...); uerr != nil {
		return res, devErr(DevErrDevPodFailed, "devpod up failed: %s", trimOutput([]byte(out)))
	}
	cid, cerr := d.containerID(ctx, t, s.Workspace, true)
	if cerr != nil || cid == "" {
		return res, devErr(DevErrDevPodFailed, "the dev container is not running after devpod up")
	}
	// Replace any previous sidecar (a rejoin brings a new one-use key).
	_, _ = d.run(ctx, t, t.docker, "rm", "-f", SidecarName(s.Workspace))
	envFile := filepath.Join(dir, "mesh.env")
	if err = os.WriteFile(envFile, []byte(SidecarEnv(s)), 0o600); err != nil {
		return res, err
	}
	defer os.Remove(envFile) // the secrets have done their job (or the attempt failed)
	if out, rerr := d.run(ctx, t, t.docker, SidecarRunArgs(s.Workspace, cid, d.cfg.MeshImage, envFile, s.Lifecycle == LifecyclePersistent)...); rerr != nil {
		return res, devErr(DevErrMeshFailed, "starting the mesh sidecar failed: %s", trimOutput([]byte(out)))
	}
	fb, err := d.awaitJoin(ctx, t, s)
	if err != nil {
		return res, err
	}
	return DevUpResult{MeshIP: fb.MeshIP, HostKeyFingerprint: fb.HostKeyFingerprint, HostKey: fb.HostKey}, nil
}

// awaitJoin reads the sidecar's log for the guest's first-boot report.
func (d *DevPod) awaitJoin(ctx context.Context, t devTools, s DevUpSpec) (FirstBoot, error) {
	to := s.Timeout
	if to <= 0 {
		to = 3 * time.Minute
	}
	deadline := d.cfg.Now().Add(to)
	for {
		out, _ := d.run(ctx, t, t.docker, "logs", "--tail", "200", SidecarName(s.Workspace))
		if fb, ok, failed, reason := scanFirstBoot(out); ok {
			return fb, nil
		} else if failed {
			return FirstBoot{}, devErr(DevErrMeshFailed, "mesh join failed: %s", reason)
		}
		if alive, _ := d.run(ctx, t, t.docker, "ps", "-q", "--filter", "name="+SidecarName(s.Workspace), "--filter", "status=running"); strings.TrimSpace(alive) == "" {
			return FirstBoot{}, devErr(DevErrMeshFailed, "the mesh sidecar exited before joining")
		}
		if !d.cfg.Now().Before(deadline) {
			return FirstBoot{}, devErr(DevErrMeshFailed, "timed out waiting for the dev container to join the network")
		}
		select {
		case <-ctx.Done():
			return FirstBoot{}, ctx.Err()
		default:
		}
		d.cfg.Sleep(2 * time.Second)
	}
}

// Alive implements DevOps.
func (d *DevPod) Alive(ctx context.Context, ws string) (bool, error) {
	if !ValidID(ws) {
		return false, errors.New("invalid workspace")
	}
	t, err := d.tools(ctx, ws, false)
	if err != nil {
		return false, err
	}
	cid, err := d.containerID(ctx, t, ws, true)
	if err != nil || cid == "" {
		return false, err
	}
	out, err := d.run(ctx, t, t.docker, "ps", "-q", "--filter", "name="+SidecarName(ws), "--filter", "status=running")
	return err == nil && strings.TrimSpace(out) != "", err
}

// Delete implements DevOps.
func (d *DevPod) Delete(ctx context.Context, ws string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.deleteLocked(ctx, ws)
}

// deleteLocked removes the sidecar, the workspace container(s) with their
// volumes, the sidecar identity volume, DevPod's own state and the secrets. Each
// step runs even if an earlier one failed; the first error is returned so the
// caller retries. If no runtime is running, the container side cannot be reached
// and that is an error (nothing is reported deleted that might still exist);
// local files are still removed.
func (d *DevPod) deleteLocked(ctx context.Context, ws string) error {
	if !ValidID(ws) {
		return errors.New("invalid workspace")
	}
	var first error
	note := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	t, terr := d.tools(ctx, ws, false)
	if terr != nil {
		note(terr)
	} else {
		_, err := d.run(ctx, t, t.docker, "rm", "-f", "-v", SidecarName(ws))
		note(ignoreMissing(err))
		if p, ok := d.cfg.Env.FindBinary("devpod"); ok {
			_, err = d.run(ctx, t, p, "delete", ws, "--force")
			note(ignoreMissing(err))
		}
		// Belt and braces: anything still carrying the workspace label.
		if out, err := d.run(ctx, t, t.docker, "ps", "-aq", "--filter", "label="+workspaceLabel+"="+ws); err == nil {
			for _, id := range strings.Fields(out) {
				_, rerr := d.run(ctx, t, t.docker, "rm", "-f", "-v", id)
				note(ignoreMissing(rerr))
			}
		} else {
			note(err)
		}
		_, err = d.run(ctx, t, t.docker, "volume", "rm", "-f", SidecarVolume(ws))
		note(ignoreMissing(err))
	}
	note(os.RemoveAll(d.wsDir(ws))) // generated source, DEVPOD_HOME, mesh.env
	return first
}

// ignoreMissing drops "No such ..." failures: the thing is already gone.
func ignoreMissing(err error) error {
	if err == nil {
		return nil
	}
	m := strings.ToLower(err.Error())
	if strings.Contains(m, "no such") || strings.Contains(m, "not found") {
		return nil
	}
	return err
}
