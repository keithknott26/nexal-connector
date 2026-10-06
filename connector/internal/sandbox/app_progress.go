package sandbox

import (
	"strconv"
	"strings"
	"time"
)

// App-profile setup runs inside the guest after it has joined the mesh (the host is already
// running). The setup script writes its progress to the console:
//
//	NEXAL-APP-STEP <app-packages|app-download|app-start> <percent>
//	NEXAL-APP-READY <profile>
//	NEXAL-APP-FAILED <reason>
//
// The runner forwards the latest of these to the coordinator, which shows it in the apps.

// AppProgress is the newest app-setup line found in console text.
type AppProgress struct {
	Step    string
	Percent int
	Ready   bool
	Failed  bool
	Reason  string
}

var appSteps = map[string]bool{"app-packages": true, "app-download": true, "app-start": true}

// ParseAppProgress scans console text; the newest recognised line wins. ok is false when none is found.
func ParseAppProgress(text string) (p AppProgress, ok bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(strings.TrimRight(lines[i], "\r"))
		if j := strings.Index(line, "NEXAL-APP-"); j > 0 {
			line = line[j:] // console prefixes (timestamps, cloud-init tags)
		}
		switch {
		case strings.HasPrefix(line, "NEXAL-APP-READY"):
			return AppProgress{Ready: true, Percent: 100}, true
		case strings.HasPrefix(line, "NEXAL-APP-FAILED"):
			reason := strings.TrimSpace(strings.TrimPrefix(line, "NEXAL-APP-FAILED"))
			if len(reason) > 200 {
				reason = reason[:200]
			}
			return AppProgress{Failed: true, Reason: reason}, true
		case strings.HasPrefix(line, "NEXAL-APP-STEP "):
			f := strings.Fields(line)
			if len(f) == 3 && appSteps[f[1]] {
				if pct, err := strconv.Atoi(f[2]); err == nil && pct >= 0 && pct <= 100 {
					return AppProgress{Step: f[1], Percent: pct}, true
				}
			}
		}
	}
	return p, false
}

// appSetupWatch bounds how long a running host's app setup is followed.
const appSetupWatch = 45 * time.Minute

// watchAppSetup follows an app-profile host's setup on its console and reports each new step,
// then a plain running report (which clears the progress) once the app is ready. Best effort:
// it stops when the host goes away, the watch window ends, or the runner shuts down.
func (m *Manager) watchAppSetup(id string) {
	deadline := time.Now().Add(appSetupWatch)
	last := AppProgress{Percent: -1}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		r := m.boxes[id]
		alive := r != nil && r.State == StateRunning
		m.mu.Unlock()
		if !alive {
			return
		}
		text, err := readTail(m.consoleLog(id), 256<<10)
		if err != nil {
			continue
		}
		p, ok := ParseAppProgress(text)
		if !ok || p == last {
			continue
		}
		last = p
		switch {
		case p.Ready:
			m.report(StateReport{SandboxID: id, State: StateRunning})
			return
		case p.Failed:
			m.opts.Logger.Warn("app setup failed in the guest", "sandbox", id, "reason", p.Reason)
			m.report(StateReport{SandboxID: id, State: StateRunning, Step: "app-failed", Percent: 0})
			return
		default:
			m.report(StateReport{SandboxID: id, State: StateRunning, Step: p.Step, Percent: p.Percent})
		}
	}
}
