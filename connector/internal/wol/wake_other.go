//go:build !darwin

package wol

// WakeForNetwork is macOS-only; elsewhere it is always false.
func WakeForNetwork() bool { return false }
