package sandbox

import (
	"context"
	"errors"
	"os"
	"time"
)

// Lifecycle and power handling for the Manager (v2 contract):
//
//   - persistent sandboxes keep their disk/workspace, hostname and SSH host key
//     across Mac restarts and never expire; ephemeral ones are wiped and
//     re-created from their image on every restart/stop and expire;
//   - sandboxes sleep with the Mac (no keep-awake assertion). The runner tells
//     the coordinator awake:false/true (sandbox-hosting-state), and after a wake
//     asks for a fresh mesh key (StateReport.NeedsKey -> rejoin task) when the
//     mesh peer was probably dropped.

// devDefaultSize is what a dev container counts against the caps (and is
// limited to) when the task carries no resources: the coordinator's smallest
// container preset. Containers share the Mac's kernel and need far less than a
// VM.
var devDefaultSize = Size{CPUs: 1, MemoryMB: 512, DiskGB: 4}

// rejoinAfterGap is the sleep length after which the ephemeral mesh peer is
// assumed gone (the control plane drops offline ephemeral peers after ~10 min).
const rejoinAfterGap = 8 * time.Minute

// heartbeatEvery is how often running sandboxes are re-reported (the
// coordinator fails a sandbox that is silent for 15 minutes while awake).
const heartbeatEvery = 30 * time.Second

// BootInfo is the non-secret part of a create task, persisted so an ephemeral
// sandbox can be re-created from its image and a persistent one restarted.
type BootInfo struct {
	Image          Image         `json:"image"`
	Size           Size          `json:"size"`
	Hostname       string        `json:"hostname"`
	Desktop        bool          `json:"desktop,omitempty"`
	SSHPublicKeys  []string      `json:"sshPublicKeys,omitempty"`
	SSHCAPublicKey string        `json:"sshCaPublicKey,omitempty"`
	DriveMode      string        `json:"driveMode,omitempty"`
	Reach          string        `json:"reach,omitempty"`
	SandboxKind    SandboxKind   `json:"sandboxKind,omitempty"`
	Lifecycle      Lifecycle     `json:"lifecycle,omitempty"`
	Devcontainer   *Devcontainer `json:"devcontainer,omitempty"`
	ExpiresAt      *time.Time    `json:"expiresAt,omitempty"`
}

func kindOf(t Task) SandboxKind {
	if t.IsDev() {
		return SandboxDevcontainer
	}
	return SandboxVM
}

func lifecycleOf(t Task) Lifecycle {
	if t.IsPersistent() {
		return LifecyclePersistent
	}
	return LifecycleEphemeral
}

// driveWritable: explicit mode wins; a persistent host defaults to read-write
// (§12.1), an ephemeral one to read-only.
func driveWritable(t Task) bool {
	switch t.DriveMode {
	case "rw":
		return true
	case "ro":
		return false
	}
	return t.IsPersistent()
}

func driveModeOf(t Task) string { return driveMode(driveWritable(t)) }

func bootInfoFrom(t Task) *BootInfo {
	b := &BootInfo{Image: t.Image, Size: t.Size, Hostname: t.Hostname, Desktop: t.Desktop,
		SSHPublicKeys: append([]string(nil), t.SSHPublicKeys...), SSHCAPublicKey: t.SSHCAPublicKey,
		DriveMode: driveModeOf(t), Reach: t.Reach, SandboxKind: kindOf(t), Lifecycle: lifecycleOf(t),
		ExpiresAt: t.ExpiresAt}
	if t.Devcontainer != nil {
		d := *t.Devcontainer
		b.Devcontainer = &d
	}
	return b
}

// rebootTask rebuilds a create-like task from the stored boot info and the fresh
// secrets in a rejoin task. It is pure.
func rebootTask(b BootInfo, t Task) Task {
	return Task{TaskID: t.TaskID, SandboxID: t.SandboxID, Kind: KindReset, Image: b.Image, Size: b.Size,
		SetupKey: t.SetupKey, VNCPassword: t.VNCPassword, SSHPublicKeys: b.SSHPublicKeys, Hostname: b.Hostname,
		Desktop: b.Desktop, ExpiresAt: b.ExpiresAt, Reach: b.Reach, SandboxKind: b.SandboxKind,
		Lifecycle: b.Lifecycle, Devcontainer: b.Devcontainer, SSHCAPublicKey: b.SSHCAPublicKey,
		DriveMode: b.DriveMode, DriveToken: t.DriveToken, ManagementURL: t.ManagementURL, DriveURL: t.DriveURL,
		keepData:  b.SandboxKind == SandboxDevcontainer && b.Lifecycle == LifecyclePersistent,
		ackRejoin: t.Kind == KindRejoin}
}

// HostingPublisher is implemented by the coordinator client: PUT sandbox-hosting.
type HostingPublisher interface {
	PutSandboxHosting(ctx context.Context, hostID string, c HostingConfig) error
}

// HostingStateReporter is implemented by the coordinator client:
// POST sandbox-hosting-state.
type HostingStateReporter interface {
	ReportSandboxHostingState(ctx context.Context, hostID string, awake, onBattery bool) error
}

// Nudge is signalled when the agent should poll right away (after a wake).
func (m *Manager) Nudge() <-chan struct{} { return m.nudgeCh }

func (m *Manager) signalNudge() {
	select {
	case m.nudgeCh <- struct{}{}:
	default:
	}
}

// ---- opt-in config ----

// refreshConfig re-reads the Mac app's sandbox-hosting.json and publishes it to
// the coordinator (PUT sandbox-hosting) when it changed. A missing file means
// "never opted in": nothing is applied or published.
func (m *Manager) refreshConfig(ctx context.Context, hostID string) {
	if m.cfgw == nil {
		return
	}
	cfg, changed, err := m.cfgw.Changed()
	if changed {
		if err != nil {
			m.opts.Logger.Warn("sandbox hosting config ignored", "error", err.Error())
		}
		m.SetCaps(cfg.Caps())
		_, serr := os.Stat(m.cfgw.path)
		m.mu.Lock()
		if serr == nil {
			c := cfg
			m.pub, m.pubDone, m.pubNext = &c, false, time.Time{}
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	pending := m.pub != nil && !m.pubDone && !m.opts.Now().Before(m.pubNext)
	var c HostingConfig
	if pending {
		c = *m.pub
	}
	coord := m.coord
	m.mu.Unlock()
	if !pending {
		return
	}
	pubr, ok := coord.(HostingPublisher)
	if !ok {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if f, ferr := m.opts.Host.Facts(pctx, m.opts.DataDir); ferr == nil {
		c.MaxCPUs, c.MaxMemoryMB = offerable(m.Caps(), f)
	}
	err = pubr.PutSandboxHosting(pctx, hostID, c)
	m.mu.Lock()
	if err == nil {
		m.pubDone = true
	} else {
		m.pubNext = m.opts.Now().Add(time.Minute)
	}
	m.mu.Unlock()
}

// ---- heartbeat ----

// heartbeat re-reports running sandboxes about every 30 s (and sandboxes that
// are waiting for a mesh key, so the coordinator mints one). It runs in a
// goroutine so a slow coordinator never delays polling.
func (m *Manager) heartbeat() {
	now := m.opts.Now()
	m.mu.Lock()
	if m.hbBusy || (!m.lastHB.IsZero() && now.Sub(m.lastHB) < heartbeatEvery) {
		m.mu.Unlock()
		return
	}
	m.lastHB = now
	var reps []StateReport
	for _, r := range m.boxes {
		if r.busy {
			continue
		}
		if r.State == StateRunning || (r.State == StateProvisioning && r.AwaitKey) {
			reps = append(reps, StateReport{SandboxID: r.ID, State: r.State, MeshIP: r.MeshIP,
				HostKeyFingerprint: r.HostKey, HostKey: r.HostKeyPub, NeedsKey: r.AwaitKey})
		}
	}
	if len(reps) == 0 {
		m.mu.Unlock()
		return
	}
	m.hbBusy = true
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for _, r := range reps {
			m.report(r)
		}
		m.mu.Lock()
		m.hbBusy = false
		m.mu.Unlock()
	}()
}

// ---- task claiming for new task kinds ----

func (m *Manager) claim(taskID, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.boxes[id]
	if rec == nil || rec.busy || (taskID != "" && m.inflight[taskID]) {
		return false
	}
	rec.busy = true
	if taskID != "" {
		m.inflight[taskID] = true
	}
	return true
}

func (m *Manager) release(taskID, id string) {
	m.mu.Lock()
	if taskID != "" {
		delete(m.inflight, taskID)
	}
	if r := m.boxes[id]; r != nil {
		r.busy = false
	}
	m.mu.Unlock()
}

// ---- vnc-password ----

// startVNC sets the one-time VNC password in a running VM through the guest
// channel and acknowledges the task. The task repeats until acknowledged, so a
// failure here simply retries on the next poll.
func (m *Manager) startVNC(t Task) {
	if !validSecret(t.Password) {
		m.opts.Logger.Warn("vnc-password task rejected", "task", t, "error", "invalid password")
		return
	}
	m.mu.Lock()
	rec := m.boxes[t.SandboxID]
	if rec == nil || rec.Kind == SandboxDevcontainer || m.inflight[t.TaskID] ||
		(rec.State != StateRunning && rec.State != StatePaused) {
		m.mu.Unlock()
		return
	}
	m.inflight[t.TaskID] = true
	h, ip, hk, hpub := rec.Handle, rec.MeshIP, rec.HostKey, rec.HostKeyPub
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.inflight, t.TaskID)
			m.mu.Unlock()
		}()
		_, err := m.opts.Guest.Send(m.ctx, h, GuestCommand{Op: GuestOpVNCPassword, Password: t.Password})
		if err != nil {
			m.opts.Logger.Info("vnc password not delivered", "task", t, "error", err.Error())
			return
		}
		m.report(StateReport{SandboxID: t.SandboxID, State: StateRunning, MeshIP: ip,
			HostKeyFingerprint: hk, HostKey: hpub, AckTaskID: t.TaskID})
	}()
}

// ---- rejoin ----

// markAwaitKey records that the mesh peer needs a fresh one-use key and asks the
// coordinator for one. It never changes the sandbox's data.
func (m *Manager) markAwaitKey(id, msg string) {
	m.mu.Lock()
	r := m.boxes[id]
	if r == nil {
		m.mu.Unlock()
		return
	}
	r.AwaitKey = true
	m.saveLocked()
	rep := StateReport{SandboxID: id, State: r.State, MeshIP: r.MeshIP, HostKeyFingerprint: r.HostKey, HostKey: r.HostKeyPub,
		NeedsKey: true, Error: msg}
	m.mu.Unlock()
	m.report(rep)
}

// startRejoin applies a fresh mesh key. Dev containers and ephemeral sandboxes
// whose VM is gone are re-created from their image; a live VM (or a persistent
// one, restarted first) is re-joined in place through the guest channel.
func (m *Manager) startRejoin(ctx context.Context, t Task) {
	if !validSecret(t.SetupKey) {
		m.opts.Logger.Warn("rejoin task rejected", "task", t, "error", "invalid setup key")
		return
	}
	m.mu.Lock()
	rec := m.boxes[t.SandboxID]
	if rec == nil || rec.busy || m.inflight[t.TaskID] {
		m.mu.Unlock()
		return // unknown or busy
	}
	if !rec.AwaitKey || rec.Boot == nil {
		// Not waiting for a key: a repeat of a task already applied. Acknowledge it
		// so the coordinator stops redelivering (the unused one-use key is dropped).
		var rep *StateReport
		if rec.State == StateRunning {
			rep = &StateReport{SandboxID: t.SandboxID, State: StateRunning, MeshIP: rec.MeshIP,
				HostKeyFingerprint: rec.HostKey, HostKey: rec.HostKeyPub, AckTaskID: t.TaskID}
		}
		m.mu.Unlock()
		if rep != nil {
			m.report(*rep)
		}
		return
	}
	kind, lc, h, ws, boot := rec.Kind, rec.Lifecycle, rec.Handle, rec.Workspace, *rec.Boot
	m.mu.Unlock()

	alive := m.isAlive(ctx, kind, h, ws)
	if kind == SandboxDevcontainer || (!alive && lc != LifecyclePersistent) {
		m.startBoot(ctx, rebootTask(boot, t))
		return
	}
	if !m.claim(t.TaskID, t.SandboxID) {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.release(t.TaskID, t.SandboxID)
		m.rejoinVM(m.ctx, t, alive)
	}()
}

func (m *Manager) rejoinVM(ctx context.Context, t Task, alive bool) {
	id := t.SandboxID
	if !alive {
		if _, err := m.restartVM(ctx, id); err != nil {
			m.markAwaitKey(id, err.Error())
			return
		}
	}
	rep, err := m.opts.Guest.Send(ctx, m.handleOf(id), GuestCommand{Op: GuestOpMeshRejoin, Key: t.SetupKey})
	if err != nil {
		m.markAwaitKey(id, "rejoin failed: "+err.Error())
		return
	}
	m.markRunning(id, rep.MeshIP, t.TaskID)
}

// markRunning sets a sandbox running (clearing AwaitKey) and reports it.
func (m *Manager) markRunning(id, meshIP, ackTask string) {
	m.mu.Lock()
	r := m.boxes[id]
	if r == nil {
		m.mu.Unlock()
		return
	}
	r.AwaitKey = false
	if meshIP != "" {
		r.MeshIP = meshIP
	}
	if r.State != StateRunning && CanTransition(r.State, StateRunning) {
		_ = m.setStateLocked(r, StateRunning)
	} else {
		m.saveLocked()
	}
	rep := StateReport{SandboxID: id, State: StateRunning, MeshIP: r.MeshIP, HostKeyFingerprint: r.HostKey, HostKey: r.HostKeyPub,
		AckTaskID: ackTask}
	m.mu.Unlock()
	m.report(rep)
}

// restartVM starts a persistent VM again from its existing disk (no seed: first
// boot already happened) and waits for the guest agent to answer.
func (m *Manager) restartVM(ctx context.Context, id string) (GuestReply, error) {
	m.mu.Lock()
	rec := m.boxes[id]
	if rec == nil || rec.Boot == nil {
		m.mu.Unlock()
		return GuestReply{}, errors.New("sandbox unknown")
	}
	old, size, host, desktop := rec.Handle, rec.Size, rec.Hostname, rec.Boot.Desktop
	timeout := m.caps.FirstBootTimeout
	m.mu.Unlock()
	if _, err := os.Stat(m.diskPath(id)); err != nil {
		return GuestReply{}, errors.New("the persistent disk is missing")
	}
	if old.Label != "" {
		_ = m.opts.Hypervisor.Kill(ctx, old) // unload a stale job so the label is free
	}
	h, err := m.opts.Hypervisor.Start(ctx, Spec{SandboxID: id, Hostname: host, CPUs: size.CPUs,
		MemoryMB: size.MemoryMB, DiskPath: m.diskPath(id), ConsoleLog: m.consoleLog(id), Desktop: desktop,
		KeepAwake: false})
	if err != nil {
		return GuestReply{}, err
	}
	m.mu.Lock()
	if r := m.boxes[id]; r != nil {
		r.Handle = h
		m.saveLocked()
	}
	m.mu.Unlock()
	deadline := m.opts.Now().Add(timeout)
	for {
		rep, err := m.opts.Guest.Send(ctx, h, GuestCommand{Op: GuestOpMeshStatus})
		if err == nil {
			return rep, nil
		}
		if !m.opts.Now().Before(deadline) {
			return GuestReply{}, errors.New("the guest did not come back after the restart")
		}
		select {
		case <-ctx.Done():
			return GuestReply{}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// ---- reconcile (connector restart, wake) ----

func (m *Manager) isAlive(ctx context.Context, kind SandboxKind, h Handle, ws string) bool {
	if kind == SandboxDevcontainer {
		if m.opts.Dev == nil || ws == "" {
			return false
		}
		ok, err := m.opts.Dev.Alive(ctx, ws)
		return err == nil && ok
	}
	if h.Label == "" {
		return false
	}
	alive, err := m.opts.Hypervisor.Alive(ctx, h)
	return err != nil || alive // an unreadable status is not evidence the VM is gone
}

// reconcile brings one persisted sandbox in line with reality after a connector
// restart (gap 0) or a wake (gap = time asleep):
//   - mid-provisioning/stopping work is failed, as before;
//   - a VM/container that is gone (the Mac rebooted) is re-created from its image
//     if ephemeral, restarted with its disk kept if persistent;
//   - a live one resumes; after a long sleep it asks for a fresh mesh key.
func (m *Manager) reconcile(ctx context.Context, id string, gap time.Duration) {
	m.mu.Lock()
	rec := m.boxes[id]
	if rec == nil || rec.busy {
		m.mu.Unlock()
		return
	}
	st, h, kind, ws, await := rec.State, rec.Handle, rec.Kind, rec.Workspace, rec.AwaitKey
	m.mu.Unlock()
	switch st {
	case StateProvisioning:
		if !await {
			m.failRecord(id, "connector restarted during provisioning")
		}
		return
	case StateStopping:
		m.failRecord(id, "connector restarted during stopping")
		return
	case StateRunning, StatePaused:
	default:
		return
	}
	if !m.isAlive(ctx, kind, h, ws) {
		m.onGone(ctx, id)
		return
	}
	m.mu.Lock()
	if r := m.boxes[id]; r != nil && r.State == StatePaused {
		_ = m.setStateLocked(r, StateRunning)
	}
	m.mu.Unlock()
	if await || gap <= 0 {
		return
	}
	if gap >= rejoinAfterGap {
		m.markAwaitKey(id, "")
		return
	}
	if kind != SandboxDevcontainer {
		m.probeMesh(id)
	}
}

// onGone handles a sandbox whose VM/container no longer runs.
func (m *Manager) onGone(ctx context.Context, id string) {
	m.mu.Lock()
	rec := m.boxes[id]
	if rec == nil || rec.busy {
		m.mu.Unlock()
		return
	}
	lc, kind, h := rec.Lifecycle, rec.Kind, rec.Handle
	m.mu.Unlock()

	if lc != LifecyclePersistent {
		// Ephemeral: never keep state across a restart. Wipe now; the rejoin task
		// (fresh key) re-creates it from the image.
		if kind != SandboxDevcontainer {
			m.killVM(h)
		}
		m.removeBoxFiles(id)
		m.mu.Lock()
		if r := m.boxes[id]; r != nil {
			r.Handle = Handle{}
			if CanTransition(r.State, StateProvisioning) {
				_ = m.setStateLocked(r, StateProvisioning)
			}
		}
		m.mu.Unlock()
		m.markAwaitKey(id, "")
		return
	}
	if kind == SandboxDevcontainer {
		// Persistent dev container: the workspace is kept; the rejoin task restarts it.
		m.mu.Lock()
		if r := m.boxes[id]; r != nil && CanTransition(r.State, StateProvisioning) {
			_ = m.setStateLocked(r, StateProvisioning)
		}
		m.mu.Unlock()
		m.markAwaitKey(id, "")
		return
	}
	// Persistent VM: restart it from its disk in the background.
	if !m.claim("", id) {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.release("", id)
		rep, err := m.restartVM(m.ctx, id)
		switch {
		case err != nil:
			m.markAwaitKey(id, err.Error())
		case rep.Connected:
			m.markRunning(id, rep.MeshIP, "")
		default:
			m.markAwaitKey(id, "")
		}
	}()
}

// probeMesh checks, for about 90 s after a wake, that a VM's mesh connection is
// back; if it never is, the peer was dropped and a fresh key is requested.
func (m *Manager) probeMesh(id string) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for i := 0; i < 6; i++ {
			select {
			case <-m.ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
			m.mu.Lock()
			r := m.boxes[id]
			if r == nil || r.AwaitKey || r.busy || r.State != StateRunning {
				m.mu.Unlock()
				return
			}
			h := r.Handle
			m.mu.Unlock()
			rep, err := m.opts.Guest.Send(m.ctx, h, GuestCommand{Op: GuestOpMeshStatus})
			if err != nil || rep.Connected {
				return // unknown (old image) or healthy
			}
		}
		m.markAwaitKey(id, "")
	}()
}

// ---- dev containers ----

// bootDev provisions a dev container with the DevOps backend.
func (m *Manager) bootDev(ctx context.Context, t Task, reset bool) error {
	ws := DevWorkspaceID(t.SandboxID)
	if t.Devcontainer == nil {
		return errors.New("devcontainer payload missing")
	}
	res, err := m.opts.Dev.Up(ctx, DevUpSpec{Workspace: ws, Hostname: t.Hostname, Devcontainer: *t.Devcontainer,
		Size: t.Size, SetupKey: t.SetupKey, ManagementURL: t.ManagementURL, SSHCAPublicKey: t.SSHCAPublicKey,
		DriveMode: driveModeOf(t), DriveToken: t.DriveToken, Lifecycle: lifecycleOf(t),
		Recreate: reset && !t.keepData, Timeout: m.firstBootTimeout()})
	if err != nil {
		return err
	}
	m.mu.Lock()
	r := m.boxes[t.SandboxID]
	if r == nil {
		m.mu.Unlock()
		return errors.New("sandbox vanished during boot")
	}
	r.Workspace, r.MeshIP, r.HostKey, r.AwaitKey = ws, res.MeshIP, res.HostKeyFingerprint, false
	r.HostKeyPub, r.DriveUnavailable = res.HostKey, res.DriveUnavailable
	err = m.setStateLocked(r, StateRunning)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if res.DriveUnavailable != "" {
		m.opts.Logger.Info("dev container has no shared drive", "sandbox", t.SandboxID, "reason", res.DriveUnavailable)
	}
	// TODO(coordinator): send res.DriveUnavailable as "driveUnavailable" once
	// POST sandbox-state accepts that key (it rejects unknown keys today).
	m.report(StateReport{SandboxID: t.SandboxID, State: StateRunning, MeshIP: res.MeshIP,
		HostKeyFingerprint: res.HostKeyFingerprint, HostKey: res.HostKey, AckTaskID: ackIf(t.ackRejoin, t.TaskID)})
	return nil
}

func (m *Manager) firstBootTimeout() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.caps.FirstBootTimeout
}

// ---- power: suspend with the host ----

// Suspend marks running sandboxes paused (the lid closed; the Mac is about to
// sleep). Nothing is stopped: the OS suspends the VMs with the host.
func (m *Manager) Suspend() {
	m.mu.Lock()
	for _, r := range m.boxes {
		if r.State == StateRunning && !r.busy {
			_ = m.setStateLocked(r, StatePaused)
		}
	}
	m.mu.Unlock()
}

// Resume runs after a wake (gap = time asleep; 0 when the Mac never slept):
// expiry is enforced in wall-clock time first, then every sandbox is reconciled
// and the agent is nudged to poll and report immediately.
func (m *Manager) Resume(gap time.Duration) {
	ctx := context.WithoutCancel(m.ctx)
	m.mu.Lock()
	m.lastHB = time.Time{}
	ids := make([]string, 0, len(m.boxes))
	for id, r := range m.boxes {
		if r.State == StateRunning || r.State == StatePaused {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	m.reap()
	for _, id := range ids {
		m.reconcile(ctx, id, gap)
	}
	m.signalNudge()
}

func (m *Manager) reportPower(ctx context.Context, awake, onBattery bool) error {
	m.mu.Lock()
	coord, hostID := m.coord, m.hostID
	m.mu.Unlock()
	r, ok := coord.(HostingStateReporter)
	if !ok || hostID == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return r.ReportSandboxHostingState(cctx, hostID, awake, onBattery)
}

func onBatteryNow(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := execRunner(ctx, "/usr/bin/pmset", "-g", "batt")
	return err == nil && ParseOnBattery(string(out))
}

// RunPower watches for sleep and wake until ctx ends. It never takes a
// keep-awake assertion. See power.go for how sleep and wake are detected.
func (m *Manager) RunPower(ctx context.Context) {
	var det SleepDetector
	var trk PowerTracker
	det.Check(time.Now())
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

	wantAwake := true
	var sentKnown, sentAwake, sentBatt bool
	batt := false
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		n++
		now := time.Now()
		if gap := det.Check(now); gap > 0 {
			trk.Woke()
			wantAwake = true
			batt = onBatteryNow(ctx)
			m.opts.Logger.Info("host woke", "asleepSeconds", int(gap.Seconds()))
			if err := m.reportPower(ctx, true, batt); err == nil {
				sentKnown, sentAwake, sentBatt = true, true, batt
			}
			m.Resume(gap)
			continue
		}
		if closed, ok := m.opts.Lid(ctx); ok {
			switch trk.Observe(closed, now) {
			case PowerAnnounceSleep:
				wantAwake = false
				m.Suspend()
			case PowerAnnounceAwake:
				wantAwake = true
				m.Resume(0)
			}
		}
		if n%20 == 1 {
			batt = onBatteryNow(ctx)
		}
		if !sentKnown || sentAwake != wantAwake || sentBatt != batt {
			if err := m.reportPower(ctx, wantAwake, batt); err == nil {
				sentKnown, sentAwake, sentBatt = true, wantAwake, batt
			}
		}
	}
}

// ackIf returns id when cond holds, else "" (for StateReport.AckTaskID).
func ackIf(cond bool, id string) string {
	if cond {
		return id
	}
	return ""
}

// offerable is the largest single instance this Mac would admit when idle.
func offerable(c Caps, f HostFacts) (cpus, memoryMB int) {
	return int(float64(f.CPUs)*c.MaxCPUFraction + 1e-9), int(float64(f.MemoryMB)*c.MaxMemFraction + 1e-9)
}
