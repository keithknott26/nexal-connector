package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"nexal/connector/internal/sandbox"
)

// newSandboxManager builds the throwaway-host runner for this Mac: image cache,
// the Virtualization.framework hypervisor (a per-VM launchd job running
// nexal-vmhost), the DevPod dev-container backend, and caps from the owner's
// sandbox-hosting.json. It returns nil off macOS or when it cannot be built; the
// connector then simply does not host sandboxes.
//
// Nothing here opts the Mac in: caps start disabled until the owner's file says
// otherwise, and a disabled, idle runner never contacts the coordinator for
// sandbox tasks (see sandbox.Manager.Poll). See internal/sandbox/README.md.
func newSandboxManager(logger *slog.Logger) *sandbox.Manager {
	if runtime.GOOS == "linux" && os.Getenv(managedRunnerEnv) == "1" {
		return newManagedSandboxManager(logger)
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	log := logger.With("component", "sandbox")
	cfgPath, err := sandbox.DefaultHostingConfigPath()
	if err != nil {
		log.Warn("sandbox hosting unavailable", "error", err.Error())
		return nil
	}
	cfg, err := sandbox.LoadHostingConfig(cfgPath)
	if err != nil {
		log.Warn("sandbox hosting config ignored", "error", err.Error())
	}
	imgDir, err := sandbox.DefaultImageDir()
	if err != nil {
		log.Warn("sandbox hosting unavailable", "error", err.Error())
		return nil
	}
	base := filepath.Dir(cfgPath) // ~/Library/Application Support/Nexal
	// NEXAL_VMHOST, when set, wins; otherwise the hypervisor searches the app bundle,
	// ~/Library/Application Support/Nexal/bin and common tool directories on every start.
	vmhost := os.Getenv("NEXAL_VMHOST")
	if vmhost == "" {
		if p, _ := sandbox.ResolveVMHost("", sandbox.VMHostCandidates(base)); p != "" {
			vmhost = p
			log.Info("nexal-vmhost found", "path", p)
		} else {
			log.Warn("nexal-vmhost not found yet; instances need it (it is searched again on each start)")
		}
	}
	// unix socket paths are short-limited on macOS; keep them out of "Application Support".
	sockDir := filepath.Join("/private/tmp", fmt.Sprintf("nexal-vm-%d", os.Getuid()))
	hv := sandbox.NewVZHypervisorWithSockets(vmhost, filepath.Join(base, "sandboxes", "run"), sockDir)

	devDir, err := sandbox.DefaultDevStateDir()
	if err != nil {
		log.Warn("dev containers unavailable", "error", err.Error())
	}
	var dev sandbox.DevOps
	if err == nil {
		dev = sandbox.NewDevPod(sandbox.DevConfig{StateDir: devDir, MeshImage: sandbox.ResolveMeshImage(os.Getenv(sandbox.MeshImageEnv))})
	}

	m, err := sandbox.NewManager(sandbox.Options{
		Caps:              cfg.Caps(),
		Images:            sandbox.NewImageStore(imgDir, sandbox.QCOW2Converter{}),
		Hypervisor:        hv,
		Dev:               dev,
		HostingConfigPath: cfgPath,
		Logger:            log,
	})
	if err != nil {
		log.Warn("sandbox hosting unavailable", "error", err.Error())
		return nil
	}
	return m
}

// Managed runner (neXal storage): a Linux server that runs dev containers for
// many tenants. Off unless the service sets NEXAL_MANAGED_RUNNER=1; see
// connector/deploy/nexal-storage/README.md.
const (
	managedRunnerEnv  = "NEXAL_MANAGED_RUNNER"
	managedStateEnv   = "NEXAL_MANAGED_STATE_DIR"   // default /var/lib/nexal
	managedConfigEnv  = "NEXAL_MANAGED_HOSTING"     // default /etc/nexal/sandbox-hosting.json
	managedSliceEnv   = "NEXAL_MANAGED_SLICE"       // default nexal-tenants; "none" disables --cgroup-parent
	managedStorageEnv = "NEXAL_MANAGED_STORAGE_OPT" // "1": --storage-opt size=<disk>G (xfs + pquota)
	managedPidsEnv    = "NEXAL_MANAGED_PIDS_LIMIT"  // per dev container, default 4096
	dockerSocketEnv   = "NEXAL_DOCKER_SOCKET"       // e.g. rootless /run/user/<uid>/docker.sock
)

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func newManagedSandboxManager(logger *slog.Logger) *sandbox.Manager {
	log := logger.With("component", "sandbox", "mode", "managed")
	base := envOr(managedStateEnv, "/var/lib/nexal")
	cfgPath := envOr(managedConfigEnv, "/etc/nexal/sandbox-hosting.json")
	cfg, err := sandbox.LoadHostingConfig(cfgPath)
	if err != nil {
		log.Warn("sandbox hosting config ignored", "error", err.Error())
	}
	if !cfg.Managed {
		log.Warn("sandbox-hosting.json has no \"managed\": true; the managed limits (up to 64 instances) need it", "path", cfgPath)
	}
	mc := sandbox.DefaultManagedConfig()
	if v := os.Getenv(managedSliceEnv); v == "none" {
		mc.SlicePrefix = ""
	} else if v != "" {
		mc.SlicePrefix = v
	}
	mc.StorageOpt = os.Getenv(managedStorageEnv) == "1"
	if v, err := strconv.Atoi(os.Getenv(managedPidsEnv)); err == nil && v > 0 {
		mc.PidsLimit = v
	}
	env := sandbox.SystemDevEnv()
	env.DockerSocket = os.Getenv(dockerSocketEnv)
	dev := sandbox.NewDevPod(sandbox.DevConfig{Env: env, StateDir: filepath.Join(base, "devcontainers"),
		MeshImage: sandbox.ResolveMeshImage(os.Getenv(sandbox.MeshImageEnv)), Managed: &mc})
	m, err := sandbox.NewManager(sandbox.Options{
		Caps:              cfg.Caps(),
		DataDir:           filepath.Join(base, "sandboxes"),
		Host:              sandbox.ProcHost{},
		Images:            sandbox.NewImageStore(filepath.Join(base, "images"), sandbox.QCOW2Converter{}),
		Hypervisor:        sandbox.NoVMHypervisor{},
		ISO:               sandbox.NoISO,
		Lid:               sandbox.NoLid,
		Dev:               dev,
		HostingConfigPath: cfgPath,
		ContainersOnly:    true,
		Logger:            log,
	})
	if err != nil {
		log.Warn("managed sandbox runner unavailable", "error", err.Error())
		return nil
	}
	log.Info("managed dev-container runner ready", "state", base, "config", cfgPath, "slice", mc.SlicePrefix)
	return m
}
