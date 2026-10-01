//go:build !unix

package config

import "os"

// ownerUID is unavailable off Unix.
func ownerUID(os.FileInfo) (int, bool) { return 0, false }
