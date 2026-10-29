//go:build unix

package inference

import (
	"os/exec"
	"testing"
	"time"
)

func TestStopAllTerminatesProcessesAndReturnsPids(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }() // reap so the pid does not linger as a zombie
	t.Cleanup(func() { cmd.Process.Kill() })

	got := stopAll([]int{cmd.Process.Pid})
	if len(got) != 1 || got[0] != cmd.Process.Pid {
		t.Fatalf("stopAll returned %v, want [%d]", got, cmd.Process.Pid)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process still running after stopAll")
	}
}

func TestStopAllEmptyIsNoop(t *testing.T) {
	if got := stopAll(nil); len(got) != 0 {
		t.Fatalf("stopAll(nil) = %v, want empty", got)
	}
}
