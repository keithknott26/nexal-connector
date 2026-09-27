//go:build darwin || linux

package privateruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func lock(root string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil
}
func runProcess(ctx context.Context, directory, session string) error {
	cmd := exec.CommandContext(ctx, filepath.Join(directory, "runtime"), "--session-file", session)
	cmd.Dir = directory
	// No inherited host token, Keychain access token, signing secret or provider key.
	cmd.Env = []string{"PATH=" + filepath.Join(directory, "bin") + ":/usr/bin:/bin", "HOME=" + directory, "TMPDIR=" + directory, "NEXAL_CONNECTOR_SESSION_FILE=" + session, "PYTHONDONTWRITEBYTECODE=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return cmd.Wait()
}
