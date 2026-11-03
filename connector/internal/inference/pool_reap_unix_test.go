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
