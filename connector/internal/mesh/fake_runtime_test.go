package mesh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFakeRuntime places a shell script as the sibling nexal-network the
// production code trusts, and removes it afterwards. It skips when that would be
// masked or collide with something real on the machine.
func installFakeRuntime(t *testing.T, script string) {
	t.Helper()
	for _, fallback := range []string{"/usr/local/bin/netbird", "/opt/homebrew/bin/netbird"} {
		if _, err := os.Stat(fallback); err == nil {
			t.Skipf("%s exists; fallback would mask the fake runtime", fallback)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot locate test executable")
	}
	sibling := filepath.Join(filepath.Dir(self), "nexal-network")
	if _, err := os.Lstat(sibling); err == nil {
		t.Skip("sibling already exists")
	}
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Skipf("cannot create sibling: %v", err)
	}
	t.Cleanup(func() { os.Remove(sibling) })
}

func TestExecRunnerSucceedsAndReportsFailureOutput(t *testing.T) {
	installFakeRuntime(t, `if [ "$1" = ok ]; then exit 0; fi
echo "daemon is not running"
exit 3`)

	if err := (ExecRunner{}).Run(context.Background(), "nexal-network", "ok"); err != nil {
		t.Fatalf("Run on a zero exit = %v, want nil", err)
	}
	err := (ExecRunner{}).Run(context.Background(), "nexal-network", "fail")
	var re *RunError
	if !errors.As(err, &re) {
		t.Fatalf("Run on a non-zero exit = %v, want *RunError", err)
	}
	if !strings.Contains(re.Output, "daemon is not running") {
		t.Errorf("RunError.Output = %q, want the runtime's own output", re.Output)
	}
}

func TestReadExitNodeStatusParsesRuntimeOutput(t *testing.T) {
	installFakeRuntime(t, `if [ "$1 $2" != "networks ls" ]; then echo bad args; exit 2; fi
printf 'Available Networks:\n  - ID: exit\n    Network: 0.0.0.0/0\n    Status: Selected\n'`)

	if got := ReadExitNodeStatus(context.Background()); got != "selected" {
		t.Errorf("ReadExitNodeStatus = %q, want selected", got)
	}
}

func TestReadExitNodeStatusUnknownWhenRuntimeFails(t *testing.T) {
	installFakeRuntime(t, `printf 'Available Networks:\n  - ID: exit\n    Network: 0.0.0.0/0\n    Status: Selected\n'
exit 1`)

	if got := ReadExitNodeStatus(context.Background()); got != "unknown" {
		t.Errorf("ReadExitNodeStatus on a failing runtime = %q, want unknown", got)
	}
}
