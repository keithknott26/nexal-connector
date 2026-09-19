//go:build darwin || linux

package runtimebridge

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func trustedInfo(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == uint32(os.Getuid()) || stat.Uid == 0) && info.Mode().Perm()&0022 == 0
}

func canonicalLocal(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	return localPath(path) && err == nil && resolved == path
}

func checkLocalDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !canonicalLocal(path) || !info.IsDir() || !trustedInfo(info) {
		return ErrPolicy
	}
	return nil
}

func openLocalFile(path string, limit int, private, executable bool) (*os.File, error) {
	if limit < 0 || !canonicalLocal(path) {
		return nil, ErrInput
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInput
	}
	f := os.NewFile(uintptr(fd), "local-input")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !trustedInfo(info) || info.Size() > int64(limit) ||
		(private && info.Mode().Perm()&0077 != 0) || (executable && info.Mode().Perm()&0111 == 0) {
		_ = f.Close()
		return nil, ErrInput
	}
	return f, nil
}

func readLocalFile(path string, limit int, private, executable bool) ([]byte, error) {
	f, err := openLocalFile(path, limit, private, executable)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, ErrInput
	}
	return data, nil
}

func acquireConfigLock(path string) (func(), error) {
	f, err := openLocalFile(path, 64*1024, true, false)
	if err != nil {
		return nil, ErrPolicy
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, ErrBusy
		}
		return nil, ErrPolicy
	}
	// The lock belongs to this descriptor and is released on close/crash.
	// No PID files, stale-lock deletion, or filesystem writes are involved.
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func prepareProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return os.ErrProcessDone
	}
	return err
}
