//go:build unix

package inference

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ReapOrphanPoolServers stops pool servers left behind by an earlier run (see orphanPoolServers)
// and returns their process ids. They hold the model in memory and the ring ports, so a new pool
// would otherwise start beside them. It asks politely first and only then forces.
func ReapOrphanPoolServers(inferenceDir string) []int {
	out, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil
	}
	pids := orphanPoolServers(string(out), inferenceDir, os.Getpid())
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(3 * time.Second); len(pids) > 0 && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		alive := false
		for _, pid := range pids {
			if syscall.Kill(pid, 0) == nil {
				alive = true
			}
		}
		if !alive {
			return pids
		}
	}
	for _, pid := range pids {
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return pids
}
