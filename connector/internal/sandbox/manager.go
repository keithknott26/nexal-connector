package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options configures a Manager. Zero values get defaults; Coordinator may be
// bound later with BindCoordinator.
type Options struct {
	Caps        Caps
	DataDir     string // default ~/Library/Application Support/Nexal/sandboxes
	Host        Host
	Images      *ImageStore
	Hypervisor  Hypervisor
	Coordinator Coordinator
	ISO         ISOBuilder
	Logger      *slog.Logger
	Now         func() time.Time

	// Dev runs kind=devcontainer sandboxes; nil means dev containers are refused.
	Dev DevOps
	// Guest talks to running VMs (vnc-password, mesh rejoin); default SocketGuestChannel.
	Guest GuestChannel
	// HostingConfigPath is the Mac app's sandbox-hosting.json; it is re-read on
	// every poll so an opt-in change applies without a restart. Empty disables it.
	HostingConfigPath string
	// Lid reads the lid state for the sleep heuristic; default IORegLid.
	Lid LidReader
	// ContainersOnly refuses VM tasks (a Linux managed host: dev containers only).
	ContainersOnly bool
}

// record is the persisted view of one sandbox.
type record struct {
	ID             string      `json:"id"`
	State          State       `json:"state"`
	Size           Size        `json:"size"`
	Hostname       string      `json:"hostname"`
	Handle         Handle      `json:"handle"`
	MeshIP         string      `json:"meshIp,omitempty"`
	ExpiresAt      *time.Time  `json:"expiresAt,omitempty"`
	CleanupPending bool        `json:"cleanupPending,omitempty"`
	UpdatedAt      time.Time   `json:"updatedAt"`
	Kind           SandboxKind `json:"kind,omitempty"`
	Lifecycle      Lifecycle   `json:"lifecycle,omitempty"`
	Boot           *BootInfo   `json:"boot,omitempty"`
	HostKey        string      `json:"hostKey,omitempty"` // SHA256 fingerprint (historic name)
	// Fields the Mac app reads from state.json (ThrowawayHosting.swift); they are
	// derived or maintained here, never trusted on load.
	Name       string     `json:"name,omitempty"`
	Paused     bool       `json:"paused"`
	Persistent bool       `json:"persistent"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	HostKeyPub string     `json:"hostKeyPub,omitempty"` // ssh-ed25519 public key as reported by the guest
	Workspace  string     `json:"workspace,omitempty"`  // dev container workspace id
	// DriveUnavailable is why the requested shared drive was not mounted (dev
	// containers). The coordinator's state report has no field for it yet, so it
	// is surfaced here (state.json) and in the connector log.
	DriveUnavailable string `json:"driveUnavailable,omitempty"`
	// AwaitKey: the mesh peer is gone; a rejoin task with a fresh key is needed.
	AwaitKey bool `json:"awaitKey,omitempty"`
	busy     bool // an operation goroutine owns this sandbox; not persisted
}

// Manager turns coordinator tasks into running VMs and tears them down in order.
type Manager struct {
	opts Options
	caps Caps

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	coord     Coordinator
	hostID    string
	boxes     map[string]*record
	inflight  map[string]bool // task ids being handled
	recovered bool

	cfgw    *configWatcher
	pub     *HostingConfig // last config read from the file, to publish
	pubDone bool
	pubNext time.Time
	lastHB  time.Time
	hbBusy  bool
	nudgeCh chan struct{}
}

// NewManager builds a Manager and loads persisted sandbox records.
func NewManager(o Options) (*Manager, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Host == nil {
		o.Host = SystemHost{}
	}
	if o.ISO == nil {
		o.ISO = HdiutilISO
	}
	if o.Guest == nil {
		o.Guest = SocketGuestChannel{}
	}
	if o.Lid == nil {
		o.Lid = IORegLid
	}
	if o.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		o.DataDir = filepath.Join(home, "Library", "Application Support", "Nexal", "sandboxes")
	}
	if o.Images == nil {
		dir, err := DefaultImageDir()
		if err != nil {
			return nil, err
		}
		o.Images = NewImageStore(dir, QCOW2Converter{})
	}
	if o.Hypervisor == nil {
		return nil, errors.New("hypervisor required")
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{opts: o, caps: o.Caps.Normalized(), ctx: ctx, cancel: cancel, coord: o.Coordinator,
		boxes: map[string]*record{}, inflight: map[string]bool{}, nudgeCh: make(chan struct{}, 1)}
	if o.HostingConfigPath != "" {
		m.cfgw = &configWatcher{path: o.HostingConfigPath}
	}
	if b, err := os.ReadFile(m.statePath()); err == nil {
		var recs []*record
		if err := json.Unmarshal(b, &recs); err != nil {
			cancel()
			return nil, errors.New("sandbox state file is invalid")
		}
		for _, r := range recs {
			if r != nil && ValidID(r.ID) {
				m.boxes[r.ID] = r
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cancel()
		return nil, err
	}
	return m, nil
}

// SetCaps replaces the per-Mac limits (the owner changed a setting). Running
// sandboxes are not touched; new tasks see the new limits.
// Caps returns the current admission caps.
func (m *Manager) Caps() Caps {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.caps
}

func (m *Manager) SetCaps(c Caps) {
	m.mu.Lock()
	m.caps = c.Normalized()
	m.mu.Unlock()
}

// BindCoordinator installs the coordinator client if none was given.
func (m *Manager) BindCoordinator(c Coordinator) {
	m.mu.Lock()
	if m.coord == nil {
		m.coord = c
	}
	m.mu.Unlock()
}

// Close cancels in-flight provisioning and waits for operation goroutines.
// Running VMs are NOT stopped: they belong to their own launchd jobs and the
// next process adopts them from the state file.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

// Snapshot is a read-only view for the Mac app's list of hosted VMs.
type Snapshot struct {
	SandboxID string      `json:"sandboxId"`
	State     State       `json:"state"`
	Hostname  string      `json:"hostname"`
	MeshIP    string      `json:"meshIp,omitempty"`
	Size      Size        `json:"size"`
	ExpiresAt *time.Time  `json:"expiresAt,omitempty"`
	Kind      SandboxKind `json:"kind,omitempty"`
	Lifecycle Lifecycle   `json:"lifecycle,omitempty"`
	AwaitKey  bool        `json:"awaitKey,omitempty"`
}

// List returns the sandboxes this Mac hosts, sorted by id.
func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.boxes))
	for _, r := range m.boxes {
		out = append(out, Snapshot{SandboxID: r.ID, State: r.State, Hostname: r.Hostname, MeshIP: r.MeshIP,
			Size: r.Size, ExpiresAt: r.ExpiresAt, Kind: r.Kind, Lifecycle: r.Lifecycle, AwaitKey: r.AwaitKey})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SandboxID < out[j].SandboxID })
	return out
}

// Kill is the owner's kill switch: tear the sandbox down now, without waiting
// for a coordinator task.
func (m *Manager) Kill(id string) error {
	if !ValidID(id) {
		return errors.New("invalid sandbox id")
	}
	if !m.startTeardown("owner-kill-"+id, id) {
		return errors.New("sandbox is busy or unknown")
	}
	return nil
}

// ---- paths ----

func (m *Manager) statePath() string { return filepath.Join(m.opts.DataDir, "state.json") }
func (m *Manager) boxDir(id string) string {
	return filepath.Join(m.opts.DataDir, "boxes", id)
}
func (m *Manager) diskPath(id string) string { return filepath.Join(m.boxDir(id), "disk.raw") }
func (m *Manager) seedDir(id string) string  { return filepath.Join(m.boxDir(id), "seed") }
func (m *Manager) seedISO(id string) string  { return filepath.Join(m.boxDir(id), "seed.iso") }
func (m *Manager) consoleLog(id string) string {
	return filepath.Join(m.boxDir(id), "console.log")
}

// saveLocked writes the state file atomically. Caller holds m.mu.
func (m *Manager) saveLocked() {
	recs := make([]*record, 0, len(m.boxes))
	for _, r := range m.boxes {
		r.Name, r.Paused, r.Persistent = r.Hostname, r.State == StatePaused, r.Lifecycle == LifecyclePersistent
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return
	}
	tmp := m.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		m.opts.Logger.Warn("sandbox state not saved", "error", err.Error())
		return
	}
	if err := os.Rename(tmp, m.statePath()); err != nil {
		m.opts.Logger.Warn("sandbox state not saved", "error", err.Error())
	}
}

// setStateLocked applies a legal transition. Caller holds m.mu.
func (m *Manager) setStateLocked(r *record, to State) error {
	if !CanTransition(r.State, to) {
		return fmt.Errorf("illegal sandbox transition %q -> %q", r.State, to)
	}
	r.State = to
	r.UpdatedAt = m.opts.Now()
	switch to {
	case StateProvisioning:
		r.StartedAt = nil
	case StateRunning:
		if r.StartedAt == nil {
			now := r.UpdatedAt
			r.StartedAt = &now
		}
	}
	m.saveLocked()
	return nil
}

// ---- coordinator I/O ----

func (m *Manager) report(r StateReport) {
	m.mu.Lock()
	coord, hostID := m.coord, m.hostID
	m.mu.Unlock()
	if coord == nil || hostID == "" {
		return
	}
	if len(r.Error) > 200 {
		r.Error = strings.ToValidUTF8(r.Error[:200], "")
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(m.ctx), 10*time.Second)
		err = coord.ReportSandboxState(ctx, hostID, r)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	m.opts.Logger.Warn("sandbox state report failed", "sandbox", r.SandboxID, "state", string(r.State), "error", err.Error())
}

// Poll fetches tasks, dispatches them, and runs housekeeping (expiry, pending
// cleanup, recovery). The agent calls it every few seconds; it never blocks on
// long work, which runs in goroutines.
func (m *Manager) Poll(ctx context.Context, hostID string) error {
	m.refreshConfig(ctx, hostID)
	m.mu.Lock()
	m.hostID = hostID
	coord := m.coord
	enabled := m.caps.Enabled
	idle := len(m.boxes) == 0
	needRecover := !m.recovered
	m.recovered = true
	m.mu.Unlock()
	if coord == nil {
		return errors.New("sandbox coordinator not bound")
	}
	if needRecover {
		m.recover()
	}
	m.reap()
	if !enabled && idle {
		return nil // opted out and nothing to look after: stay off the network
	}
	m.heartbeat()
	tasks, err := coord.SandboxTasks(ctx, hostID)
	if err != nil {
		return err
	}
	// Deletes first: they free capacity for the creates behind them.
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Kind == KindDelete && tasks[j].Kind != KindDelete })
	for _, t := range tasks {
		m.dispatch(ctx, t)
	}
	return nil
}

func (m *Manager) usageLocked() Usage {
	var u Usage
	for _, r := range m.boxes {
		if r.State.Active() {
			u.Count++
			u.CPUs += r.Size.CPUs
			u.MemoryMB += r.Size.MemoryMB
		}
	}
	return u
}

func (m *Manager) dispatch(ctx context.Context, t Task) {
	if err := ValidateTask(t); err != nil {
		m.opts.Logger.Warn("sandbox task rejected", "task", t, "error", err.Error())
		return
	}
	switch t.Kind {
	case KindDelete:
		m.startTeardown(t.TaskID, t.SandboxID)
	case KindCreate, KindReset:
		m.startBoot(ctx, t)
	case KindVNCPassword:
		m.startVNC(t)
	case KindRejoin:
		m.startRejoin(ctx, t)
	}
}

// ---- create / reset ----

func (m *Manager) startBoot(ctx context.Context, t Task) {
	dev := t.IsDev()
	if err := ValidateBoot(t); err != nil {
		m.fail(t.SandboxID, "invalid task: "+err.Error())
		return
	}
	if dev {
		if m.opts.Dev == nil {
			m.fail(t.SandboxID, devErr(DevErrNotAvailable, "dev containers are not available on this Mac").Error())
			return
		}
		if t.Size == (Size{}) {
			t.Size = devDefaultSize
		}
	} else if m.opts.ContainersOnly {
		m.fail(t.SandboxID, devErr(DevErrNotAvailable, "this host runs dev containers only").Error())
		return
	} else if err := ValidateImage(t.Image, HostArch()); err != nil {
		m.fail(t.SandboxID, err.Error())
		return
	}
	if t.IsPersistent() {
		t.ExpiresAt = nil // persistent sandboxes never expire
	}
	if t.ExpiresAt != nil && !t.ExpiresAt.After(m.opts.Now()) {
		m.fail(t.SandboxID, "task already expired")
		return
	}
	reset := t.Kind == KindReset
	// Host facts are gathered before taking the lock: they run subprocesses.
	facts, ferr := m.opts.Host.Facts(ctx, m.opts.DataDir)

	m.mu.Lock()
	if m.inflight[t.TaskID] {
		m.mu.Unlock()
		return
	}
	rec := m.boxes[t.SandboxID]
	switch {
	case reset && (rec == nil || rec.busy || rec.State == StateStopping || rec.State == StateDeleted):
		m.mu.Unlock()
		return
	case !reset && rec != nil && rec.State != StateDeleted:
		m.mu.Unlock()
		return // create is idempotent: already known
	}
	usage := m.usageLocked()
	if reset && rec.State.Active() {
		// A reset reuses the sandbox's own slot.
		usage.Count--
		usage.CPUs -= rec.Size.CPUs
		usage.MemoryMB -= rec.Size.MemoryMB
	}
	var admit error
	if ferr != nil {
		admit = refuse(RefusePowerUnknown, "cannot read host facts: %v", ferr)
	} else {
		admit = AdmitCaps(m.caps, facts, usage, t.Size)
	}
	if admit != nil {
		m.mu.Unlock()
		m.fail(t.SandboxID, admit.Error())
		return
	}
	if rec == nil || !reset {
		rec = &record{ID: t.SandboxID}
		m.boxes[t.SandboxID] = rec
	}
	rec.Size, rec.Hostname, rec.ExpiresAt = t.Size, t.Hostname, t.ExpiresAt
	rec.Kind, rec.Lifecycle, rec.Boot = kindOf(t), lifecycleOf(t), bootInfoFrom(t)
	rec.AwaitKey, rec.HostKey, rec.HostKeyPub, rec.DriveUnavailable = false, "", "", ""
	if dev {
		rec.Workspace = DevWorkspaceID(t.SandboxID)
	}
	rec.busy = true
	m.inflight[t.TaskID] = true
	if err := m.setStateLocked(rec, StateProvisioning); err != nil {
		rec.busy = false
		delete(m.inflight, t.TaskID)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.inflight, t.TaskID)
			if r := m.boxes[t.SandboxID]; r != nil {
				r.busy = false
			}
			m.mu.Unlock()
		}()
		m.report(StateReport{SandboxID: t.SandboxID, State: StateProvisioning})
		if err := m.boot(m.ctx, t, reset); err != nil {
			m.opts.Logger.Warn("sandbox boot failed", "task", t, "error", err.Error())
			if t.keepData {
				m.markAwaitKey(t.SandboxID, err.Error()) // never wipe a persistent workspace
			} else {
				m.failRecord(t.SandboxID, err.Error())
			}
		}
	}()
}

// boot runs the provisioning pipeline: image -> disk clone -> seed -> start ->
// wait for the guest's first-boot report -> running -> delete seed.
func (m *Manager) boot(ctx context.Context, t Task, reset bool) error {
	if t.IsDev() {
		return m.bootDev(ctx, t, reset)
	}
	id := t.SandboxID
	progress := throttle(func(step string, pct int) { m.reportProgress(id, step, pct) })
	ctx = WithProgress(ctx, progress)
	progress(StepCheck, 2)
	if reset {
		// Stop the old VM (ordered), then drop its disk and seed.
		m.stopVM(ctx, m.handleOf(id))
		m.removeBoxFiles(id)
	}
	// Disk space: an advance check from the mirror's size, then the real one.
	if sz := m.opts.Images.RemoteSize(ctx, t.Image.URL); sz > 0 {
		if err := m.diskCheck(ctx, uint64(sz)); err != nil {
			return err
		}
	}
	base, size, err := m.opts.Images.Ensure(ctx, t.Image)
	if err != nil {
		return err
	}
	if err := m.diskCheck(ctx, uint64(size)); err != nil {
		return err
	}
	progress(StepDisk, 76)
	if err := os.MkdirAll(m.boxDir(id), 0o700); err != nil {
		return err
	}
	if err := CloneDisk(ctx, base, m.diskPath(id), t.Size.DiskGB); err != nil {
		return err
	}
	seed := SeedParams{SandboxID: id, InstanceID: fmt.Sprintf("%s-%d", id, m.opts.Now().Unix()),
		Hostname: t.Hostname, SetupKey: t.SetupKey, VNCPassword: t.VNCPassword,
		SSHPublicKeys: t.SSHPublicKeys, Desktop: t.Desktop,
		DriveWritable: driveWritable(t), SSHCAPublicKey: t.SSHCAPublicKey, DriveToken: t.DriveToken,
		Lifecycle: lifecycleOf(t), ManagementURL: t.ManagementURL, DriveURL: t.DriveURL}
	progress(StepSeed, 82)
	if err := WriteSeedDir(m.seedDir(id), seed); err != nil {
		return err
	}
	if err := m.opts.ISO(ctx, m.seedDir(id), m.seedISO(id)); err != nil {
		return err
	}
	// The plaintext seed directory is no longer needed once the ISO exists.
	_ = os.RemoveAll(m.seedDir(id))
	_ = os.Remove(m.consoleLog(id))

	progress(StepBoot, 88)
	h, err := m.opts.Hypervisor.Start(ctx, Spec{SandboxID: id, Hostname: t.Hostname, CPUs: t.Size.CPUs,
		MemoryMB: t.Size.MemoryMB, DiskPath: m.diskPath(id), SeedPath: m.seedISO(id),
		ConsoleLog: m.consoleLog(id), Desktop: t.Desktop, KeepAwake: false})
	if err != nil {
		return err
	}
	m.mu.Lock()
	if r := m.boxes[id]; r != nil {
		r.Handle = h
		m.saveLocked()
	}
	m.mu.Unlock()

	progress(StepJoin, 93)
	fb, err := m.awaitFirstBoot(ctx, id, h)
	if err != nil {
		return err
	}
	m.mu.Lock()
	r := m.boxes[id]
	if r == nil {
		m.mu.Unlock()
		return errors.New("sandbox vanished during boot")
	}
	r.MeshIP, r.HostKey, r.HostKeyPub, r.AwaitKey = fb.MeshIP, fb.HostKeyFingerprint, fb.HostKey, false
	err = m.setStateLocked(r, StateRunning)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// First-boot report received: the secrets have done their job.
	if err := RemoveSeed(m.seedDir(id), m.seedISO(id)); err != nil {
		m.opts.Logger.Warn("seed not removed", "sandbox", id, "error", err.Error())
	}
	m.report(StateReport{SandboxID: id, State: StateRunning, MeshIP: fb.MeshIP, HostKeyFingerprint: fb.HostKeyFingerprint, HostKey: fb.HostKey,
		AckTaskID: ackIf(t.ackRejoin, t.TaskID)})
	return nil
}

func (m *Manager) diskCheck(ctx context.Context, imageBytes uint64) error {
	f, err := m.opts.Host.Facts(ctx, m.opts.DataDir)
	if err != nil {
		return err
	}
	m.mu.Lock()
	c := m.caps
	m.mu.Unlock()
	return CheckDisk(c, f.FreeDisk, imageBytes)
}

func (m *Manager) handleOf(id string) Handle {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.boxes[id]; r != nil {
		return r.Handle
	}
	return Handle{}
}

// awaitFirstBoot tails the console log for the guest's report.
func (m *Manager) awaitFirstBoot(ctx context.Context, id string, h Handle) (FirstBoot, error) {
	m.mu.Lock()
	timeout := m.caps.FirstBootTimeout
	m.mu.Unlock()
	deadline := m.opts.Now().Add(timeout)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return FirstBoot{}, ctx.Err()
		case <-t.C:
		}
		if text, err := readTail(m.consoleLog(id), 256<<10); err == nil {
			fb, ok, failed, reason := scanFirstBoot(text)
			if ok {
				return fb, nil
			}
			if failed {
				return FirstBoot{}, fmt.Errorf("guest first-boot failed: %s", reason)
			}
		}
		if alive, err := m.opts.Hypervisor.Alive(ctx, h); err == nil && !alive {
			return FirstBoot{}, errors.New("VM exited before first boot completed")
		}
		if m.opts.Now().After(deadline) {
			return FirstBoot{}, errors.New("timed out waiting for the guest to join the network")
		}
	}
}

// readTail returns up to max trailing bytes of a file.
func readTail(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.Size() > max {
		if _, err := f.Seek(fi.Size()-max, io.SeekStart); err != nil {
			return "", err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	return string(b), err
}

// ---- failure ----

// fail reports a refusal/failure for a sandbox that has no record (or whose
// record must stay untouched), e.g. an admission refusal.
func (m *Manager) fail(id, msg string) {
	m.opts.Logger.Info("sandbox task refused", "sandbox", id, "reason", msg)
	m.report(StateReport{SandboxID: id, State: StateFailed, Error: msg})
}

// failRecord moves an existing sandbox to failed: the VM is killed and the disk
// and seed are removed (a failed sandbox holds no resources), then the failure is
// reported. The record stays so a later delete task is acknowledged.
func (m *Manager) failRecord(id, msg string) {
	m.killVM(m.handleOf(id))
	m.removeBoxFiles(id)
	m.mu.Lock()
	if r := m.boxes[id]; r != nil {
		if CanTransition(r.State, StateFailed) {
			_ = m.setStateLocked(r, StateFailed)
		}
		r.Handle = Handle{}
	}
	m.saveLocked()
	m.mu.Unlock()
	m.report(StateReport{SandboxID: id, State: StateFailed, Error: msg})
}

// ---- teardown ----

// startTeardown claims the sandbox and runs the ordered teardown in a goroutine.
// It returns false if the sandbox is busy; unknown sandboxes are acknowledged as
// deleted (idempotent delete).
func (m *Manager) startTeardown(taskID, id string) bool {
	m.mu.Lock()
	if m.inflight[taskID] {
		m.mu.Unlock()
		return false
	}
	rec := m.boxes[id]
	if rec == nil {
		m.mu.Unlock()
		m.report(StateReport{SandboxID: id, State: StateDeleted})
		return true
	}
	if rec.busy {
		m.mu.Unlock()
		return false
	}
	if rec.State == StateDeleted && !rec.CleanupPending {
		delete(m.boxes, id)
		m.saveLocked()
		m.mu.Unlock()
		m.report(StateReport{SandboxID: id, State: StateDeleted})
		return true
	}
	rec.busy = true
	m.inflight[taskID] = true
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.inflight, taskID)
			if r := m.boxes[id]; r != nil {
				r.busy = false
			}
			m.mu.Unlock()
		}()
		m.teardown(id)
	}()
	return true
}

// teardown is the ordered spin-down (§8a): ACPI stop -> force stop after the
// grace period -> report -> remove disk and seed. If removal fails it is retried
// by reap, so a disk is never forgotten.
func (m *Manager) teardown(id string) {
	ctx := context.WithoutCancel(m.ctx) // teardown must finish even during shutdown
	m.mu.Lock()
	rec := m.boxes[id]
	if rec == nil {
		m.mu.Unlock()
		return
	}
	h := rec.Handle
	dev := rec.Kind == SandboxDevcontainer
	if rec.State != StateDeleted {
		if err := m.setStateLocked(rec, StateStopping); err != nil {
			m.mu.Unlock()
			m.opts.Logger.Warn("teardown refused", "sandbox", id, "error", err.Error())
			return
		}
		m.mu.Unlock()
		m.report(StateReport{SandboxID: id, State: StateStopping})
	} else {
		m.mu.Unlock()
	}

	if !dev {
		m.stopVM(ctx, h)
	} // a dev container is removed by removeBoxFilesErr below

	m.mu.Lock()
	if r := m.boxes[id]; r != nil {
		if r.State != StateDeleted {
			_ = m.setStateLocked(r, StateDeleted)
		}
		r.Handle = Handle{}
		r.CleanupPending = true
		m.saveLocked()
	}
	m.mu.Unlock()
	m.report(StateReport{SandboxID: id, State: StateDeleted})

	if err := m.removeBoxFilesErr(id); err != nil {
		m.opts.Logger.Warn("sandbox files not removed; will retry", "sandbox", id, "error", err.Error())
		return
	}
	m.mu.Lock()
	delete(m.boxes, id)
	m.saveLocked()
	m.mu.Unlock()
}

// stopVM requests an ACPI shutdown, waits StopGrace for the VM to exit, then
// force-kills it. It is safe on an empty Handle.
func (m *Manager) stopVM(ctx context.Context, h Handle) {
	if h.Label == "" {
		return
	}
	hv := m.opts.Hypervisor
	m.mu.Lock()
	grace := m.caps.StopGrace
	m.mu.Unlock()
	if err := hv.Stop(ctx, h); err != nil {
		m.opts.Logger.Info("graceful stop not delivered; forcing", "sandbox", h.SandboxID, "error", err.Error())
	} else {
		deadline := time.Now().Add(grace)
		for time.Now().Before(deadline) {
			if alive, err := hv.Alive(ctx, h); err == nil && !alive {
				_ = hv.Kill(ctx, h) // unload the launchd job and remove its files
				return
			}
			time.Sleep(time.Second)
		}
	}
	m.killVM(h)
}

func (m *Manager) killVM(h Handle) {
	if h.Label == "" {
		return
	}
	if err := m.opts.Hypervisor.Kill(context.WithoutCancel(m.ctx), h); err != nil {
		m.opts.Logger.Warn("force stop failed", "sandbox", h.SandboxID, "error", err.Error())
	}
}

func (m *Manager) removeBoxFiles(id string) {
	if err := m.removeBoxFilesErr(id); err != nil {
		m.opts.Logger.Warn("sandbox files not removed", "sandbox", id, "error", err.Error())
	}
}

func (m *Manager) removeBoxFilesErr(id string) error {
	if !ValidID(id) {
		return errors.New("invalid sandbox id")
	}
	var derr error
	if ws := m.workspaceOf(id); ws != "" && m.opts.Dev != nil {
		// container, volumes, mesh sidecar, DevPod state, secrets
		derr = m.opts.Dev.Delete(context.WithoutCancel(m.ctx), ws)
	}
	if err := os.RemoveAll(m.boxDir(id)); err != nil { // disk, seed, seed image, console log
		return err
	}
	return derr
}

func (m *Manager) workspaceOf(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.boxes[id]; r != nil {
		return r.Workspace
	}
	return ""
}

// ---- housekeeping ----

// reap enforces expiry locally (the coordinator also sends delete tasks, but a
// runner that cannot reach it must still honour the lifetime) and retries
// cleanups that failed.
func (m *Manager) reap() {
	now := m.opts.Now()
	type job struct{ taskID, id string }
	var expire []job
	var cleanup []string
	m.mu.Lock()
	for id, r := range m.boxes {
		if r.busy {
			continue
		}
		switch {
		case r.CleanupPending:
			cleanup = append(cleanup, id)
		case (r.State == StateRunning || r.State == StatePaused) && r.Lifecycle != LifecyclePersistent &&
			r.ExpiresAt != nil && !r.ExpiresAt.After(now):
			expire = append(expire, job{"expiry-" + id, id})
		}
	}
	m.mu.Unlock()
	for _, id := range cleanup {
		if m.removeBoxFilesErr(id) == nil {
			m.mu.Lock()
			delete(m.boxes, id)
			m.saveLocked()
			m.mu.Unlock()
		}
	}
	for _, j := range expire {
		m.startTeardown(j.taskID, j.id)
	}
}

// recover adopts persisted sandboxes after a connector restart (see reconcile).
func (m *Manager) recover() {
	ctx := context.WithoutCancel(m.ctx)
	m.mu.Lock()
	ids := make([]string, 0, len(m.boxes))
	for id := range m.boxes {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		m.reconcile(ctx, id, 0)
	}
}
