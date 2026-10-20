package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// verifyWorkspaceGone inspects the runtime after a VM is removed and fails if any
// container or volume of that VM is still there, so the delete is retried.
func (d *DevPod) verifyWorkspaceGone(ctx context.Context, ws string) error {
	t, err := d.tools(ctx, ws, false)
	if err != nil {
		return err
	}
	containers, err := d.run(ctx, t, t.docker, "ps", "-aq", "--filter", "label="+workspaceLabel+"="+ws)
	if err != nil {
		return err
	}
	volumes, err := d.run(ctx, t, t.docker, "volume", "ls", "-q", "--filter", "name="+SidecarVolume(ws))
	if err != nil {
		return err
	}
	if left := strings.Fields(containers + " " + volumes); len(left) > 0 {
		return fmt.Errorf("workspace %s still has runtime items: %s", ws, strings.Join(left, " "))
	}
	return nil
}

// colimaPurgeTimeout bounds each Colima call so a wedged VM cannot block a delete.
const colimaPurgeTimeout = 3 * time.Minute

// PurgeColimaIfIdle removes the neXal-managed Colima VM and its data disk once no
// dev container workspace is left. Colima keeps its data disk (~/.colima/_lima/
// _disks/colima/datadisk) across `colima delete`, and deleting images inside it
// does not shrink the file on macOS, so without this the disk only grows.
//
// It acts only on the Colima that neXal installed (under ManagedRuntimeBin), never
// on a Colima the user runs themselves. Set NEXAL_KEEP_COLIMA=1 to opt out.
// Errors are returned for logging; the workspace delete has already succeeded.
// It also unloads and removes the systems.nexal.colima login agent.
func (d *DevPod) PurgeColimaIfIdle(ctx context.Context) error {
	if os.Getenv("NEXAL_KEEP_COLIMA") == "1" {
		return nil
	}
	bin, ok := d.cfg.Env.FindBinary("colima")
	if !ok || !strings.HasPrefix(bin, ManagedRuntimeBin(d.cfg.Env.Home)+string(filepath.Separator)) {
		return nil
	}
	busy, err := hasWorkspaceDirs(d.cfg.StateDir)
	if err != nil || busy {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, colimaPurgeTimeout)
	defer cancel()
	// Stop first so nothing writes to the disk; then delete the VM and its data disk.
	_, _ = d.cfg.Env.Run(ctx, nil, bin, "stop")
	_, err = d.cfg.Env.Run(ctx, nil, bin, "delete", "--force", "--data")
	if err := ignoreMissing(err); err != nil {
		return err
	}
	// Unload the login agent so Colima does not come back at the next login. The
	// installer re-registers it the next time a runtime is needed.
	label := "systems.nexal.colima"
	_, _ = d.cfg.Env.Run(ctx, nil, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label))
	if home, herr := os.UserHomeDir(); herr == nil {
		_ = os.Remove(filepath.Join(home, "Library", "LaunchAgents", label+".plist"))
	}
	return nil
}

// hasWorkspaceDirs reports whether any workspace state directory remains.
func hasWorkspaceDirs(stateDir string) (bool, error) {
	entries, err := os.ReadDir(stateDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			return true, nil
		}
	}
	return false, nil
}
