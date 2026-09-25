//go:build darwin

package wol

import (
	"context"
	"os/exec"
	"time"
)

// pmsetCustom is replaced in tests.
var pmsetCustom = func() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "custom").Output()
}

// WakeForNetwork reports whether macOS "Wake for network access" is on for
// every power source (pmset womp=1). Any failure reads as false.
func WakeForNetwork() bool {
	out, err := pmsetCustom()
	if err != nil || len(out) > 64<<10 {
		return false
	}
	return parseWakeOnMagicPacket(string(out))
}
