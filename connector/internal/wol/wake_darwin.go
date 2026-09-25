//go:build darwin

package wol

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// wakeForNetwork runs `/usr/bin/pmset -g` with the same discipline as the
// agent's diagnostic(): absolute path, no shell, a 2 s timeout, WaitDelay so a
// child holding the pipe cannot stall us, bounded output and a fixed
// environment. That helper lives in internal/agent, which imports this package,
// so the few lines are repeated here rather than creating an import cycle.
func wakeForNetwork(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/pmset", "-g")
	cmd.WaitDelay = time.Second
	out := &boundedBuffer{max: 256 << 10}
	cmd.Stdout = out
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	if err := cmd.Run(); err != nil {
		return WakeUnknown
	}
	return ParseWomp(out.Bytes())
}

type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("pmset output limit exceeded")
	}
	return b.Buffer.Write(p)
}
