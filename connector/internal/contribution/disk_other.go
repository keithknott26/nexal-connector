//go:build !linux && !darwin

package contribution

import "errors"

// FreeDiskBytes is unavailable on platforms without Statfs. It returns an error
// rather than a number, so the caller reports the disk condition as UNKNOWN —
// never as "plenty of space", which is the fabrication that would let a host
// fill an owner's volume, and never as "low", which would stop the host.
func FreeDiskBytes(string) (uint64, error) {
	return 0, errors.New("free disk space is not measurable on this platform")
}
