//go:build unix

package inference

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockInstall takes an exclusive advisory lock so two installs cannot share a
// staging directory. The lock dies with the process.
func lockInstall(root string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(root, ".install.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
