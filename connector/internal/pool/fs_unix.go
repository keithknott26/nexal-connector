//go:build linux || darwin

package pool

import (
	"os"
	"syscall"
)

func openNoFollow(root *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
	f, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || st.Nlink != 1 || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, ErrUnsafePath
	}
	return f, nil
}

func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func freeDisk(f *os.File) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	bsize := uint64(st.Bsize)
	if bsize == 0 || uint64(st.Bavail) > ^uint64(0)/bsize {
		return 0, ErrInvalid
	}
	return uint64(st.Bavail) * bsize, nil
}
