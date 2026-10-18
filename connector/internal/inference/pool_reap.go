package inference

import (
	"path/filepath"
	"strconv"
	"strings"
)

// orphanPoolServers picks, from `ps -axo pid=,ppid=,command=` output, the pool servers that a
// previous run left behind: mlx_lm servers started with --pipeline for a model under this
// inference directory whose parent is gone (a Mac that quits or kills the helper cannot stop its
// child on exit, so the server is re-parented to launchd, pid 1). A server whose helper is still
// alive has that helper as parent and is never picked, and neither is anything outside this
// directory, so a second pool or a hand-started mlx_lm is left alone.
func orphanPoolServers(ps, inferenceDir string, self int) []int {
	models := "--model " + modelsRoot(inferenceDir) + string(filepath.Separator)
	var pids []int
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil || ppid != 1 || pid == self {
			continue
		}
		cmd := strings.Join(f[2:], " ")
		if strings.Contains(cmd, " -m mlx_lm") && strings.Contains(cmd, " --pipeline") && strings.Contains(cmd, models) {
			pids = append(pids, pid)
		}
	}
	return pids
}
