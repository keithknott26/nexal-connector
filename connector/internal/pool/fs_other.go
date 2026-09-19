//go:build !linux && !darwin

package pool

import "os"

func openNoFollow(*os.Root, string, int, os.FileMode) (*os.File, error) {
	return nil, ErrUnsupported
}
func lockFile(*os.File) error           { return ErrUnsupported }
func freeDisk(*os.File) (uint64, error) { return 0, ErrUnsupported }
