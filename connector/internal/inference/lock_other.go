//go:build !unix

package inference

import (
	"os"
	"path/filepath"
)

func lockInstall(root string) (func(), error) {
	p := filepath.Join(root, ".install.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return func() { f.Close(); os.Remove(p) }, nil
}
