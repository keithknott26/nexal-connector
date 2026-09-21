//go:build linux || darwin

package contribution

import (
	"errors"
	"syscall"
)

// FreeDiskBytes reports free space on the volume containing path, using Statfs
// directly.
//
// syscall rather than golang.org/x/sys: go.mod has NO dependencies at all, and
// the house rule is that it stays that way. Statfs_t's field types differ
// between Linux and Darwin (int64 vs uint32 for Bsize among others), which is
// exactly why the conversions below are explicit and the bounds are checked.
//
// This is the one dimension of the three that is genuinely measurable in a Linux
// sandbox, so unlike power and thermal it is real and tested here rather than
// stubbed — the same choice internal/pool made in fs_unix.go, whose freeDisk
// helper this deliberately mirrors (Bavail * Bsize, overflow-checked). It uses
// Statfs on a path rather than Fstatfs on a descriptor because the caller has a
// data directory, not an open file, and must not create one just to measure.
//
// Bavail, not Bfree: Bfree includes blocks reserved for root, which a user-level
// agent can never use. Reporting space we cannot write would be the fabrication
// this whole package is built to avoid.
func FreeDiskBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		// The error is discarded rather than wrapped: it can contain the path,
		// and the caller turns any failure into Unknown anyway.
		return 0, errors.New("free disk space unavailable")
	}
	bsize := uint64(st.Bsize)
	avail := uint64(st.Bavail)
	if bsize == 0 || avail > ^uint64(0)/bsize {
		return 0, errors.New("implausible free disk telemetry")
	}
	return avail * bsize, nil
}
