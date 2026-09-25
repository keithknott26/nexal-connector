//go:build darwin

package sysinfo

import "syscall"

// dataVolume is where user data lives on APFS; "/" is the sealed system
// snapshot and reports almost no free space.
func dataVolume() string { return "/System/Volumes/Data" }

func statfs(path string) (total, free uint64, err error) {
	var s syscall.Statfs_t
	if err = syscall.Statfs(path, &s); err != nil {
		return 0, 0, err
	}
	return s.Blocks * uint64(s.Bsize), s.Bavail * uint64(s.Bsize), nil
}
