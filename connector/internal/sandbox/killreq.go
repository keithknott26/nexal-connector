package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Owner kill requests. The Mac app asks for an immediate teardown by creating an
// empty file
//
//	<DataDir>/kill-requests/<sandboxId>
//
// (~/Library/Application Support/Nexal/sandboxes/kill-requests/<id>). The runner
// polls the directory every killPollEvery, calls Kill(id) and deletes the file.
// A sandbox that is busy (provisioning, or already tearing down) keeps its
// request for the next poll, up to killRequestTTL; an unknown sandbox has nothing
// to kill, so its request is simply dropped.
const (
	killPollEvery  = 2 * time.Second
	killRequestTTL = 10 * time.Minute
)

func (m *Manager) killRequestsDir() string { return filepath.Join(m.opts.DataDir, "kill-requests") }

// ProcessKillRequests handles every pending request once and returns how many
// sandboxes it started tearing down. Only regular files named like a sandbox id
// are honored (no symlinks, nothing nested).
func (m *Manager) ProcessKillRequests() int {
	dir := m.killRequestsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	started := 0
	for _, e := range entries {
		id := e.Name()
		path := filepath.Join(dir, id)
		fi, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if !ValidID(id) || !fi.Mode().IsRegular() {
			_ = os.Remove(path) // not ours to interpret; never follow it
			continue
		}
		m.mu.Lock()
		_, known := m.boxes[id]
		m.mu.Unlock()
		if !known {
			_ = os.Remove(path)
			continue
		}
		if err := m.Kill(id); err != nil {
			if m.opts.Now().Sub(fi.ModTime()) > killRequestTTL {
				_ = os.Remove(path)
			}
			continue // busy: try again on the next poll
		}
		started++
		_ = os.Remove(path)
		m.opts.Logger.Info("owner kill requested", "sandbox", id)
	}
	return started
}

// RunKillRequests polls the kill-request directory until ctx ends.
func (m *Manager) RunKillRequests(ctx context.Context) {
	t := time.NewTicker(killPollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ProcessKillRequests()
		}
	}
}
