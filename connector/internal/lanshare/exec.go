package lanshare

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ExecRunner runs commands for real.
//
// It is the only place in this package that touches the process table, and it is
// deliberately small: argv goes straight to exec.CommandContext with no shell,
// and the environment is emptied so nothing inherited can change how a command
// about to run as root behaves.
type ExecRunner struct{}

// Run executes name with args and returns combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// An empty environment, not the inherited one. PATH is irrelevant because
	// every program is an absolute path, but variables like DYLD_INSERT_LIBRARIES
	// are not irrelevant, and inheriting them into a privileged command is how a
	// library gets loaded into a root process.
	cmd.Env = []string{}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		// The command's own output is the useful part; the exit status alone says
		// nothing actionable. Bounded so a runaway program cannot flood a terminal.
		if len(text) > 2048 {
			text = text[:2048] + "… (truncated)"
		}
		if text == "" {
			return "", err
		}
		return text, fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

// Available reports whether the system tools this package needs are present.
//
// Called before anything else so a non-macOS machine gets one clear answer
// instead of a confusing failure from the first command tried.
func Available() error {
	for _, p := range []string{SharingPath, DiskutilPath, LaunchctlPath} {
		if _, err := exec.LookPath(p); err != nil {
			return fmt.Errorf("lanshare: %s is not present; LAN Time Machine sharing requires macOS", p)
		}
	}
	return nil
}
