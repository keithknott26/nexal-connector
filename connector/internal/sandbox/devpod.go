package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
// Its join parameters (one-use setup key included) are in a 0600 file in a
// per-workspace directory bind-mounted at /run/nexal-boot, never in its
// environment or on a command line (so `docker inspect` does not show them); the
// connector deletes that directory as soon as the join is reported. The sidecar
// prints the same `NEXAL-FIRSTBOOT {"meshIp":...}` line as a VM on its log.
//
// SSH: the sidecar runs no sshd. Because the network namespace is shared, the
// connector then `docker exec`s DevSSHDScript in the DEV container (as root):
// it installs openssh-server if the image lacks it, creates login `nexal` as an
// alias of the workspace's remote user (same uid, gid and home), and starts a
// private sshd bound to the mesh IP, port 22, that trusts only the network's SSH
// CA (TrustedUserCAKeys, principal nexal), exactly like a VM. Its host key and
// fingerprint are reported in the same NEXAL-FIRSTBOOT form and returned in
// DevUpResult, so the coordinator pins them as for a VM.
//
// Shared drive: not available in dev containers (see DevDriveUnavailable).

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
	// Tenant is the coordinator's per-tenant tag; required in managed mode.
	Tenant string
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
	// DriveUnavailable is a user-safe reason when the task asked for the shared
	// drive and it was not mounted ("" when no drive was requested).
	DriveUnavailable string
}

// DevDriveUnavailable is why a dev container has no shared drive. The VM path
// mounts it with the `nexal drive` FUSE helper baked into the guest image; a
// dev container image does not carry that helper, and mounting FUSE would need
// /dev/fuse and CAP_SYS_ADMIN on the DEV container itself (the sidecar cannot
// mount into another container's mount namespace), which repo-sourced
// devcontainer.json files do not grant. The workspace still runs; the drive
// token is never handed to the dev container or the sidecar.
const DevDriveUnavailable = "the shared drive is not available in dev containers yet"

// Error codes for DevError.
const (
	DevErrNoRuntime     = "no_container_runtime"
	DevErrDockerDesktop = "docker_desktop_only"
	DevErrDevPodMissing = "devpod_missing"
	DevErrDevPodFailed  = "devpod_failed"
	DevErrMeshFailed    = "mesh_sidecar_failed"
	DevErrSSHFailed     = "ssh_setup_failed"
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
	// RuntimeDocker is the native Docker Engine on Linux (a managed server).
	RuntimeDocker RuntimeName = "docker"
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
	// GOOS is the operating system ("" behaves like macOS). On "linux" the
	// native Docker Engine socket is a supported runtime.
	GOOS string
	// DockerSocket, when set on Linux, is the only engine socket tried (e.g.
	// rootless Docker's /run/user/<uid>/docker.sock).
	DockerSocket string
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
		GOOS:     runtime.GOOS,
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
	if e.GOOS == "linux" {
		// A Linux server runs the native engine (Apache-2.0; Docker Desktop's
		// licence concern does not apply to it).
		if e.DockerSocket != "" {
			return []runtimeCandidate{{RuntimeDocker, e.DockerSocket}}
		}
		c = append(c, runtimeCandidate{RuntimeDocker, "/var/run/docker.sock"}, runtimeCandidate{RuntimeDocker, "/run/docker.sock"})
	}
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

// DefaultDevboxImage is the version-pinned "neXal devbox" image: a slim
// Debian with git, build tools, Go, Python 3 and Node LTS, and the
// openssh-server package DevSSHDScript expects. It is built from
// connector/devbox by .github/workflows/devbox-image.yml on a `devbox-v<version>`
// tag, which pushes ghcr.io/<owner>/nexal-devbox:<version>; the package must be
// public so a Mac can pull it anonymously. Bump it here whenever a new devbox
// version is released (the tag build fails if the two differ).
const DefaultDevboxImage = "ghcr.io/keithknott26/nexal-devbox:0.1.0"

// DevboxTemplate is the template the coordinator sends when the user picked no
// repository, template or devcontainer.json.
const DevboxTemplate = "devbox"

// templateImages maps the catalog templates to dev container images.
var templateImages = map[string]string{
	DevboxTemplate: DefaultDevboxImage,
	"ubuntu":       "mcr.microsoft.com/devcontainers/base:ubuntu",
	"debian":       "mcr.microsoft.com/devcontainers/base:debian",
	"node":         "mcr.microsoft.com/devcontainers/typescript-node",
	"python":       "mcr.microsoft.com/devcontainers/python",
	"go":           "mcr.microsoft.com/devcontainers/go",
	"rust":         "mcr.microsoft.com/devcontainers/rust",
}

// devPidsLimit caps processes in a dev container (a fork bomb must not take the
// Mac's container VM down); generous enough for parallel builds.
const devPidsLimit = 4096

// DevLimitArgs are the docker resource flags for a dev container of size s:
// CPUs, memory with swap capped at the same value (so --memory is a real
// ceiling), and a process limit. Disk is NOT limited per container:
// --storage-opt size= only works on overlay2 over XFS with project quotas, which
// the Colima/Lima/OrbStack/Podman VMs do not use, and docker refuses to start the
// container when it is set there. The coordinator's disk figure for a container
// is therefore an allocation used for account accounting, not an enforced
// quota. It is pure.
func DevLimitArgs(s Size) []string {
	var a []string
	if s.CPUs > 0 {
		a = append(a, fmt.Sprintf("--cpus=%d", s.CPUs))
	}
	if s.MemoryMB > 0 {
		a = append(a, fmt.Sprintf("--memory=%dm", s.MemoryMB), fmt.Sprintf("--memory-swap=%dm", s.MemoryMB))
	}
	if len(a) > 0 {
		a = append(a, fmt.Sprintf("--pids-limit=%d", devPidsLimit))
	}
	return a
}

// DevcontainerFile renders the devcontainer.json for a template or inline
// payload, with the Mac owner's resource limits added as runArgs (after any the
// payload brings, so ours win). Repo sources bring their own file and return ""
// (DevPod reads it from the checkout); Up applies the limits to those with
// `docker update` instead.
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
	for _, a := range DevLimitArgs(size) {
		args = append(args, a)
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

// SidecarBootMount is where the sidecar finds its join file (SidecarBootFile).
const (
	SidecarBootMount = "/run/nexal-boot"
	SidecarBootFile  = "mesh.env"
)

// SidecarLimitArgs keep the mesh sidecar small: it only runs the NetBird client
// (one Go process, a few tens of MB resident). Half a CPU leaves room for the
// ML-KEM/rosenpass handshakes and userspace WireGuard when the kernel module is
// unavailable, so the dev container's own traffic is not throttled; 128 MB with
// no extra swap is roughly 3x its working set.
var SidecarLimitArgs = []string{"--cpus=0.5", "--memory=128m", "--memory-swap=128m", "--pids-limit=256"}

// SidecarRunArgs builds the `docker run` arguments for the mesh sidecar. The
// secrets are in bootDir/SidecarBootFile (0600), bind-mounted at
// SidecarBootMount: they are on neither the command line nor in the
// container's environment. bootDir must not contain a comma (--mount syntax);
// Up checks that. It is pure.
func SidecarRunArgs(workspace, containerID, image, bootDir string, persistent bool, extra ...string) []string {
	a := []string{"run", "-d", "--name", SidecarName(workspace),
		"--label", "nexal.workspace=" + workspace,
		"--network", "container:" + containerID,
		"--cap-add", "NET_ADMIN", "--device", "/dev/net/tun",
		"--mount", "type=bind,src=" + bootDir + ",dst=" + SidecarBootMount}
	a = append(a, SidecarLimitArgs...)
	if persistent {
		a = append(a, "-v", SidecarVolume(workspace)+":/var/lib/nexal")
	}
	a = append(a, extra...)
	return append(a, image)
}

// SidecarEnv renders the sidecar's join file (KEY=VALUE lines, parsed by the
// entrypoint, never sourced). Values were validated by Up; the file is written
// 0600 and deleted after the join. The management URL uses the VM seed's name,
// NEXAL_MESH_URL. The SSH CA and the drive token are not the sidecar's business
// and are not written.
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
	w("NEXAL_MESH_URL", s.ManagementURL)
	return b.String()
}

// devUserPattern is a conservative login name; anything else is ignored.
var devUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// RemoteUserFromLabels picks the workspace's login user from the dev
// container's labels (`docker inspect --format '{{json .Config.Labels}}'`): the
// last remoteUser, else the last containerUser, in the devcontainer.metadata
// label. "" when unknown (DevSSHDScript then uses the uid-1000 user, else root).
// It is pure.
func RemoteUserFromLabels(labelsJSON string) string {
	var labels map[string]string
	if json.Unmarshal([]byte(strings.TrimSpace(labelsJSON)), &labels) != nil {
		return ""
	}
	var meta []map[string]any
	if json.Unmarshal([]byte(labels["devcontainer.metadata"]), &meta) != nil {
		return ""
	}
	pick := func(key string) string {
		for i := len(meta) - 1; i >= 0; i-- {
			if u, ok := meta[i][key].(string); ok && devUserPattern.MatchString(u) {
				return u
			}
		}
		return ""
	}
	if u := pick("remoteUser"); u != "" {
		return u
	}
	return pick("containerUser")
}

// DevSSHDScript runs as root inside the dev container (POSIX sh), with
// arguments: mesh IP, remote user ("" = guess), SSH CA public key. It prints
// `NEXAL-FIRSTBOOT {"meshIp":..,"hostKeyFingerprint":..,"hostKey":..}` or
// `NEXAL-FIRSTBOOT-FAILED <reason>`. The host key lives in /var/lib/nexal-ssh,
// so a persistent workspace keeps it across restarts; an ephemeral one is a new
// container (new key) every time. It is re-runnable: a previous nexal sshd is
// stopped first.
const DevSSHDScript = `set -u
umask 022
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
MESH_IP=$1 HINT=$2 CA=$3
fail() { printf 'NEXAL-FIRSTBOOT-FAILED %s
' "$*"; exit 1; }
[ "$(id -u)" = 0 ] || fail "ssh setup needs root in the dev container"
if ! command -v sshd >/dev/null 2>&1 || ! command -v ssh-keygen >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    { DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends openssh-server; } >/dev/null 2>&1
  elif command -v apk >/dev/null 2>&1; then apk add -q --no-cache openssh-server openssh-keygen >/dev/null 2>&1
  elif command -v dnf >/dev/null 2>&1; then dnf install -y -q openssh-server >/dev/null 2>&1
  elif command -v microdnf >/dev/null 2>&1; then microdnf install -y openssh-server >/dev/null 2>&1
  elif command -v yum >/dev/null 2>&1; then yum install -y -q openssh-server >/dev/null 2>&1
  elif command -v zypper >/dev/null 2>&1; then zypper -qn install openssh >/dev/null 2>&1
  elif command -v pacman >/dev/null 2>&1; then pacman -Sy --noconfirm --needed openssh >/dev/null 2>&1
  fi
fi
SSHD=$(command -v sshd 2>/dev/null || true)
[ -n "$SSHD" ] || fail "no sshd in the dev container and openssh-server could not be installed"
command -v ssh-keygen >/dev/null 2>&1 || fail "no ssh-keygen in the dev container"
U=$HINT
if [ -z "$U" ] || ! grep -q "^$U:" /etc/passwd; then U=$(awk -F: '$3==1000{print $1; exit}' /etc/passwd); fi
[ -n "$U" ] || U=root
[ "$U" != nexal ] || fail "the workspace user may not be called nexal"
ENT=$(awk -F: -v u="$U" '$1==u{print $3":"$4":"$6":"$7; exit}' /etc/passwd)
[ -n "$ENT" ] || fail "workspace user $U not found"
uid=${ENT%%:*}; r=${ENT#*:}; gid=${r%%:*}; r=${r#*:}; home=${r%%:*}; sh=${r#*:}
[ -n "$sh" ] && [ -x "$sh" ] || sh=/bin/sh
# login nexal = the workspace user (same uid/gid/home), so files and sudo rights match
for f in /etc/passwd /etc/shadow; do
  [ -f "$f" ] || continue
  { grep -v '^nexal:' "$f" || true; } >"$f.nexal" && cat "$f.nexal" >"$f"; rm -f "$f.nexal"
done
printf 'nexal:x:%s:%s:neXal mesh login:%s:%s
' "$uid" "$gid" "$home" "$sh" >>/etc/passwd
[ -f /etc/shadow ] && printf 'nexal:*:19000:0:99999:7:::
' >>/etc/shadow
prl=no; [ "$uid" = 0 ] && prl=prohibit-password
mkdir -p /var/lib/nexal-ssh /etc/nexal /run/sshd
chmod 0700 /var/lib/nexal-ssh
K=/var/lib/nexal-ssh/ssh_host_ed25519_key
[ -s "$K" ] || ssh-keygen -q -t ed25519 -N '' -C '' -f "$K" >/dev/null 2>&1 || fail "host key generation failed"
[ -s "$K.pub" ] || ssh-keygen -y -f "$K" >"$K.pub" || fail "host public key missing"
printf '%s
' "$CA" >/etc/nexal/ssh_user_ca.pub
chmod 0644 /etc/nexal/ssh_user_ca.pub
cat >/etc/nexal/sshd_config <<CFG
ListenAddress $MESH_IP:22
HostKey $K
TrustedUserCAKeys /etc/nexal/ssh_user_ca.pub
AuthorizedKeysFile none
AllowUsers nexal
PasswordAuthentication no
ChallengeResponseAuthentication no
PermitRootLogin $prl
PidFile /run/nexal-sshd.pid
Subsystem sftp internal-sftp
CFG
if [ -s /run/nexal-sshd.pid ]; then
  old=$(cat /run/nexal-sshd.pid)
  case $(cat /proc/"$old"/comm 2>/dev/null) in sshd*) kill "$old" 2>/dev/null; sleep 1 ;; esac
fi
"$SSHD" -t -f /etc/nexal/sshd_config 2>/dev/null || fail "sshd rejected the configuration"
"$SSHD" -f /etc/nexal/sshd_config || fail "sshd did not start on $MESH_IP:22"
fp=$(ssh-keygen -lf "$K.pub" -E sha256 | awk '{print $2}')
hk=$(awk 'NR==1{print $1" "$2}' "$K.pub")
printf 'NEXAL-FIRSTBOOT {"meshIp":"%s","hostKeyFingerprint":"%s","hostKey":"%s"}
' "$MESH_IP" "$fp" "$hk"
`

// DevSSHDExecArgs builds the `docker exec` that runs DevSSHDScript in the dev
// container. Its arguments are public (mesh IP, user name, CA public key) and
// were validated by the caller. It is pure.
func DevSSHDExecArgs(containerID, meshIP, user, caKey string) []string {
	return []string{"exec", "-u", "0", containerID, "sh", "-c", DevSSHDScript, "nexal-sshd", meshIP, user, caKey}
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
const DefaultMeshImage = "ghcr.io/keithknott26/nexal-mesh-sidecar:0.1.1"

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
	// Managed, when set, runs many tenants' workspaces on this host (managed.go):
	// per-tenant networks, slices and labels, and sanitized definitions.
	Managed *ManagedConfig
	Sleep   func(time.Duration)
	Now     func() time.Time
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
	// The coordinator always sends mesh.managementUrl with a key; without it the
	// sidecar would silently fall back to upstream NetBird's own server.
	if s.ManagementURL == "" || !validHTTPSURL(s.ManagementURL) {
		return res, devErr(DevErrInvalid, "the task carries no valid mesh management URL")
	}
	if s.SSHCAPublicKey != "" && !validCAKey(s.SSHCAPublicKey) {
		return res, devErr(DevErrInvalid, "invalid ssh ca public key")
	}
	if err := ValidateDevcontainer(&s.Devcontainer); err != nil {
		return res, devErr(DevErrInvalid, "%v", err)
	}
	managed := d.cfg.Managed != nil
	if managed && !ValidTenantTag(s.Tenant) {
		return res, devErr(DevErrInvalid, "a managed host needs the task's tenant tag")
	}
	var file string
	if !managed {
		if file, err = DevcontainerFile(s.Devcontainer, s.Size); err != nil {
			return res, err
		}
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
	if managed {
		if err = d.ensureManagedNetwork(ctx, t, s.Tenant); err != nil {
			return res, err
		}
		src := filepath.Join(dir, "src")
		_, statErr := os.Stat(src)
		reuse := !fresh && statErr == nil // a kept workspace: its files live in src
		if !reuse {
			_ = os.RemoveAll(src)
		}
		if _, err = d.prepareManagedSource(ctx, t, s, src, reuse); err != nil {
			return res, err
		}
		source = src
	} else if file != "" {
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
	// Enforce the size on the running container whatever its source: a repo
	// brings its own devcontainer.json without our runArgs, and a persistent
	// workspace may have been created before the limits existed.
	if lim := DevLimitArgs(s.Size); len(lim) > 0 {
		if out, lerr := d.run(ctx, t, t.docker, append(append([]string{"update"}, lim...), cid)...); lerr != nil {
			return res, devErr(DevErrDevPodFailed, "could not apply the resource limits to the dev container: %s", trimOutput([]byte(out)))
		}
	}
	// Replace any previous sidecar (a rejoin brings a new one-use key).
	_, _ = d.run(ctx, t, t.docker, "rm", "-f", SidecarName(s.Workspace))
	bootDir := filepath.Join(dir, "boot")
	if strings.Contains(bootDir, ",") {
		return res, devErr(DevErrMeshFailed, "the dev container state directory path contains a comma")
	}
	_ = os.RemoveAll(bootDir)
	if err = os.Mkdir(bootDir, 0o700); err != nil {
		return res, err
	}
	// The secrets have done their job once the join is reported (or the attempt
	// failed); the sidecar also deletes its copy when the mount is writable.
	defer os.RemoveAll(bootDir)
	if err = os.WriteFile(filepath.Join(bootDir, SidecarBootFile), []byte(SidecarEnv(s)), 0o600); err != nil {
		return res, err
	}
	var sidecarExtra []string
	if managed {
		sidecarExtra = ManagedSidecarArgs(*d.cfg.Managed, s.Tenant)
	}
	if out, rerr := d.run(ctx, t, t.docker, SidecarRunArgs(s.Workspace, cid, d.cfg.MeshImage, bootDir, s.Lifecycle == LifecyclePersistent, sidecarExtra...)...); rerr != nil {
		return res, devErr(DevErrMeshFailed, "starting the mesh sidecar failed: %s", trimOutput([]byte(out)))
	}
	fb, err := d.awaitJoin(ctx, t, s)
	_ = os.RemoveAll(bootDir)
	if err != nil {
		return res, err
	}
	res = DevUpResult{MeshIP: fb.MeshIP}
	if s.SSHCAPublicKey != "" {
		labels, _ := d.run(ctx, t, t.docker, "inspect", "--format", "{{json .Config.Labels}}", cid)
		out, _ := d.run(ctx, t, t.docker, DevSSHDExecArgs(cid, fb.MeshIP, RemoteUserFromLabels(labels), s.SSHCAPublicKey)...)
		sb, ok, failed, reason := scanFirstBoot(out)
		switch {
		case failed:
			return DevUpResult{}, devErr(DevErrSSHFailed, "ssh in the dev container: %s", reason)
		case !ok || sb.MeshIP != fb.MeshIP || sb.HostKey == "":
			return DevUpResult{}, devErr(DevErrSSHFailed, "ssh in the dev container did not report its host key")
		}
		res.HostKeyFingerprint, res.HostKey = sb.HostKeyFingerprint, sb.HostKey
	}
	if s.DriveToken != "" {
		res.DriveUnavailable = DevDriveUnavailable
	}
	return res, nil
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
		// Images DevPod built for this workspace only (features, Dockerfile or
		// the UID-update layer); found before the containers are gone.
		built := d.builtImages(ctx, t, ws)
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
		// Best effort: an image another workspace still uses stays (docker
		// refuses), and a leftover build image must not block the delete.
		for _, img := range built {
			_, _ = d.run(ctx, t, t.docker, "image", "rm", img)
		}
	}
	note(os.RemoveAll(d.wsDir(ws))) // generated source, DEVPOD_HOME, boot/mesh.env
	return first
}

// builtImages lists the images of the workspace's containers that DevPod built
// for it (devpod-* / vsc-*); shared pulled images such as the devbox are kept
// as a cache.
func (d *DevPod) builtImages(ctx context.Context, t devTools, ws string) []string {
	out, err := d.run(ctx, t, t.docker, "ps", "-aq", "--filter", "label="+workspaceLabel+"="+ws)
	if err != nil {
		return nil
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return nil
	}
	names, err := d.run(ctx, t, t.docker, append([]string{"inspect", "--format", "{{.Config.Image}}"}, ids...)...)
	if err != nil {
		return nil
	}
	return WorkspaceBuiltImages(names)
}

// WorkspaceBuiltImages picks, from `docker inspect --format {{.Config.Image}}`
// output, the per-workspace images DevPod or the devcontainer CLI built. It is
// pure.
func WorkspaceBuiltImages(inspect string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range strings.Fields(inspect) {
		base := n[strings.LastIndex(n, "/")+1:]
		if (strings.HasPrefix(base, "devpod-") || strings.HasPrefix(base, "vsc-")) && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
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
