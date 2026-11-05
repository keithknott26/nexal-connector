//go:build unix

package inference

import (
	"os/exec"
	"testing"
	"time"
)

func TestStopAllNoPidsIsANoop(t *testing.T) {
	if got := stopAll(nil); len(got) != 0 {
		t.Fatalf("stopAll(nil) = %v, want empty", got)
	}
}

func TestStopAllTerminatesRunningProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }() // reap so the pid does not linger as a zombie
	got := stopAll([]int{cmd.Process.Pid})
	if len(got) != 1 || got[0] != cmd.Process.Pid {
		t.Fatalf("stopAll returned %v, want [%d]", got, cmd.Process.Pid)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process still running after stopAll")
	}
}

// A server that ignores SIGTERM must still be gone after stopAll: the grace period expires and the
// process is force-killed, otherwise it would keep the model and ring ports held.
func TestStopAllForceKillsProcessIgnoringSIGTERM(t *testing.T) {
	cmd := exec.Command("sh", "-c", `trap "" TERM; while :; do sleep 0.1; done`)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sh: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	time.Sleep(300 * time.Millisecond) // let the shell install its trap before SIGTERM arrives
	got := stopAll([]int{cmd.Process.Pid})
	if len(got) != 1 || got[0] != cmd.Process.Pid {
		_ = cmd.Process.Kill()
		t.Fatalf("stopAll returned %v, want [%d]", got, cmd.Process.Pid)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process ignoring SIGTERM survived stopAll")
	}
}

// With no matching server under the given inference directory, both reapers must find nothing
// and must not signal any process (the test process itself is excluded by pid anyway).
func TestReapOrphansWithNoMatchingServersReturnsNothing(t *testing.T) {
	if _, err := exec.LookPath("/bin/ps"); err != nil {
		t.Skip("/bin/ps not available")
	}
	dir := t.TempDir()
	if got := ReapOrphanPoolServers(dir); len(got) != 0 {
		t.Fatalf("ReapOrphanPoolServers = %v, want none", got)
	}
	if got := ReapOrphanServeServers(dir); len(got) != 0 {
		t.Fatalf("ReapOrphanServeServers = %v, want none", got)
	}
}
