package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

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
	vmhost := os.Getenv("NEXAL_VMHOST")
	if vmhost == "" {
		if exe, err := os.Executable(); err == nil {
			vmhost = filepath.Join(filepath.Dir(exe), "nexal-vmhost")
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
		dev = sandbox.NewDevPod(sandbox.DevConfig{StateDir: devDir, MeshImage: os.Getenv("NEXAL_DEV_MESH_IMAGE")})
	}

	m, err := sandbox.NewManager(sandbox.Options{
		Caps:              cfg.Caps(),
		Images:            sandbox.NewImageStore(imgDir, nil),
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
