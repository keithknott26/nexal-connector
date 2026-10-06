//go:build !darwin

package wol

import "context"

// wakeForNetwork is unknown off macOS: there is no pmset, and guessing either
// answer would mislead the owner about whether a wake can work.
func wakeForNetwork(context.Context) string { return WakeUnknown }

func hardwarePortMACs(context.Context) []string { return nil }
