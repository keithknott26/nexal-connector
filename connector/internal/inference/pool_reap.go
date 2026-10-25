package inference

import (
	"path/filepath"
	"strconv"
	"strings"
)

// orphanPoolServers picks, from `ps -axo pid=,ppid=,command=` output, the pool servers that a
// previous run left behind: pool servers (`-m mlx_lm server`, or any --pipeline server) for a model under this
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
		if strings.Contains(cmd, models) && (strings.Contains(cmd, " -m mlx_lm server ") || (strings.Contains(cmd, " -m mlx_lm") && strings.Contains(cmd, " --pipeline"))) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// orphanServeServers picks single-Mac model servers (`nexal inference serve`, run through serveShim) whose
// helper is gone: parent pid 1, serving a model from this inference directory. serveShim's script spans
// several lines, so a line that does not start with a pid and a parent pid continues the one before it.
func orphanServeServers(ps, inferenceDir string, self int) []int {
	models := "--model " + modelsRoot(inferenceDir) + string(filepath.Separator)
	type proc struct {
		pid, ppid int
		cmd       string
	}
	var procs []proc
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 {
			pid, e1 := strconv.Atoi(f[0])
			ppid, e2 := strconv.Atoi(f[1])
			if e1 == nil && e2 == nil {
				procs = append(procs, proc{pid, ppid, strings.Join(f[2:], " ")})
				continue
			}
		}
		if len(procs) > 0 {
			procs[len(procs)-1].cmd += " " + strings.TrimSpace(line)
		}
	}
	var pids []int
	for _, p := range procs {
		if p.ppid == 1 && p.pid != self && strings.Contains(p.cmd, models) && strings.Contains(p.cmd, "from mlx_lm import server") {
			pids = append(pids, p.pid)
		}
	}
	return pids
}
