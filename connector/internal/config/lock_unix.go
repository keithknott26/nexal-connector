//go:build darwin || linux

package config

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Lock holds an advisory lock for the lifetime of the process. A second process
// cannot race the attempt journal or claim the same host under another port.
func Lock(configPath string) (func(), error) {
	path := filepath.Join(filepath.Dir(configPath), "agent.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("cannot open private connector lock")
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		_ = f.Close()
		return nil, errors.New("connector lock must be a private regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another connector process holds this configuration")
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
}
