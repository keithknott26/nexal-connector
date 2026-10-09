package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeHyp struct {
	mu     sync.Mutex
	alive  map[string]bool
	killed []string
	starts int
}

func (f *fakeHyp) Start(ctx context.Context, s Spec) (Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.alive == nil {
		f.alive = map[string]bool{}
	}
	f.alive["l-"+s.SandboxID] = true
	return Handle{SandboxID: s.SandboxID, Label: "l-" + s.SandboxID, Guest: "/nonexistent"}, nil
}
func (f *fakeHyp) Stop(ctx context.Context, h Handle) error { return nil }
func (f *fakeHyp) Kill(ctx context.Context, h Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, h.Label)
	delete(f.alive, h.Label)
	return nil
}
func (f *fakeHyp) Alive(ctx context.Context, h Handle) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[h.Label], nil
}

type fakeDev struct {
	mu      sync.Mutex
	alive   bool
	deleted []string
	ups     []DevUpSpec
}

func (f *fakeDev) Up(ctx context.Context, s DevUpSpec) (DevUpResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups = append(f.ups, s)
	return DevUpResult{MeshIP: "100.64.0.5"}, nil
}
func (f *fakeDev) Alive(ctx context.Context, ws string) (bool, error) { return f.alive, nil }
func (f *fakeDev) Delete(ctx context.Context, ws string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, ws)
	return nil
}

type fakeGuest struct {
	mu   sync.Mutex
	cmds []GuestCommand
	rep  GuestReply
}

func (f *fakeGuest) Send(ctx context.Context, h Handle, c GuestCommand) (GuestReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, c)
	return f.rep, nil
}

func newTestManager(t *testing.T, hv Hypervisor, dev DevOps, g GuestChannel) *Manager {
	t.Helper()
	dir := t.TempDir()
	m, err := NewManager(Options{DataDir: filepath.Join(dir, "sb"), Images: NewImageStore(filepath.Join(dir, "img"), nil),
		Hypervisor: hv, Dev: dev, Guest: g, Caps: Caps{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func put(m *Manager, r *record) {
	m.mu.Lock()
	m.boxes[r.ID] = r
	m.mu.Unlock()
}

func snap(m *Manager, id string) record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.boxes[id]
}

func TestRebootTaskAndDriveDefaults(t *testing.T) {
	b := bootInfoFrom(Task{SandboxID: "s", Hostname: "h", Lifecycle: LifecyclePersistent, SandboxKind: SandboxDevcontainer,
		Devcontainer: &Devcontainer{Template: "go"}, SSHPublicKeys: []string{"ssh-ed25519 A x"}})
	if b.DriveMode != "rw" || b.Lifecycle != LifecyclePersistent || b.SandboxKind != SandboxDevcontainer {
		t.Fatalf("%+v", b)
	}
	rt := rebootTask(*b, Task{TaskID: "r1", SandboxID: "s", SetupKey: "NEWKEY", DriveToken: "dt"})
	if rt.Kind != KindReset || rt.SetupKey != "NEWKEY" || rt.DriveToken != "dt" || !rt.keepData || !rt.IsDev() {
		t.Fatalf("%+v", rt)
	}
	e := bootInfoFrom(Task{SandboxID: "s", Hostname: "h"})
	if e.DriveMode != "ro" || e.Lifecycle != LifecycleEphemeral {
		t.Fatalf("%+v", e)
	}
	if rebootTask(*e, Task{TaskID: "r"}).keepData {
		t.Fatal("ephemeral must never keep data")
	}
	if driveWritable(Task{DriveMode: "ro", Lifecycle: LifecyclePersistent}) {
		t.Fatal("explicit ro wins")
	}
}

func TestEphemeralGoneIsWipedAndAsksForKey(t *testing.T) {
	hv := &fakeHyp{}
	m := newTestManager(t, hv, nil, nil)
	if err := os.MkdirAll(m.boxDir("sb1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.diskPath("sb1"), []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	put(m, &record{ID: "sb1", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecycleEphemeral,
		Handle: Handle{Label: "l1"}, Boot: &BootInfo{Hostname: "h"}})
	m.Resume(0)
	r := snap(m, "sb1")
	if r.State != StateProvisioning || !r.AwaitKey || r.Handle.Label != "" {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(m.diskPath("sb1")); !os.IsNotExist(err) {
		t.Fatal("ephemeral disk must be wiped after the VM is gone")
	}
}

func TestPersistentDevGoneKeepsWorkspace(t *testing.T) {
	dev := &fakeDev{}
	m := newTestManager(t, &fakeHyp{}, dev, nil)
	put(m, &record{ID: "sb2", State: StateRunning, Kind: SandboxDevcontainer, Lifecycle: LifecyclePersistent,
		Workspace: "nexal-sb2", Boot: &BootInfo{Hostname: "h"}})
	m.Resume(0)
	r := snap(m, "sb2")
	if !r.AwaitKey || r.State != StateProvisioning {
		t.Fatalf("%+v", r)
	}
	if len(dev.deleted) != 0 {
		t.Fatalf("persistent workspace must not be deleted: %v", dev.deleted)
	}
}

func TestEphemeralDevGoneIsDeleted(t *testing.T) {
	dev := &fakeDev{}
	m := newTestManager(t, &fakeHyp{}, dev, nil)
	put(m, &record{ID: "sb3", State: StateRunning, Kind: SandboxDevcontainer, Lifecycle: LifecycleEphemeral,
		Workspace: "nexal-sb3", Boot: &BootInfo{Hostname: "h"}})
	m.Resume(0)
	if len(dev.deleted) == 0 || dev.deleted[0] != "nexal-sb3" {
		t.Fatalf("deleted %v", dev.deleted)
	}
	if r := snap(m, "sb3"); !r.AwaitKey {
		t.Fatalf("%+v", r)
	}
}

func TestLongSleepAsksForKeyShortDoesNot(t *testing.T) {
	hv := &fakeHyp{alive: map[string]bool{"l1": true, "l2": true}}
	m := newTestManager(t, hv, nil, &fakeGuest{rep: GuestReply{OK: true, Connected: true}})
	put(m, &record{ID: "a", State: StatePaused, Kind: SandboxVM, Lifecycle: LifecycleEphemeral, Handle: Handle{Label: "l1"}})
	put(m, &record{ID: "b", State: StatePaused, Kind: SandboxVM, Lifecycle: LifecycleEphemeral, Handle: Handle{Label: "l2"}})
	m.Resume(rejoinAfterGap + time.Minute)
	if r := snap(m, "a"); !r.AwaitKey || r.State != StateRunning {
		t.Fatalf("a: %+v", r)
	}
	m2 := newTestManager(t, hv, nil, &fakeGuest{rep: GuestReply{OK: true, Connected: true}})
	put(m2, &record{ID: "b", State: StatePaused, Kind: SandboxVM, Lifecycle: LifecycleEphemeral, Handle: Handle{Label: "l2"}})
	m2.Resume(2 * time.Minute)
	if r := snap(m2, "b"); r.AwaitKey || r.State != StateRunning {
		t.Fatalf("short sleep must resume quietly: %+v", r)
	}
}

func TestSuspendPausesRunningOnly(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	put(m, &record{ID: "r", State: StateRunning})
	put(m, &record{ID: "f", State: StateFailed})
	m.Suspend()
	if snap(m, "r").State != StatePaused || snap(m, "f").State != StateFailed {
		t.Fatal("only running sandboxes pause")
	}
}

func TestRejoinInPlaceOnLivePersistentVM(t *testing.T) {
	hv := &fakeHyp{alive: map[string]bool{"l1": true}}
	g := &fakeGuest{rep: GuestReply{OK: true, MeshIP: "100.64.0.9"}}
	m := newTestManager(t, hv, nil, g)
	put(m, &record{ID: "sb4", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecyclePersistent, AwaitKey: true,
		Handle: Handle{Label: "l1"}, Boot: &BootInfo{Hostname: "h"}})
	m.startRejoin(context.Background(), Task{TaskID: "sbt_r", SandboxID: "sb4", Kind: KindRejoin, SetupKey: "FRESH"})
	m.wg.Wait()
	r := snap(m, "sb4")
	if r.AwaitKey || r.MeshIP != "100.64.0.9" || r.State != StateRunning {
		t.Fatalf("%+v", r)
	}
	if len(g.cmds) != 1 || g.cmds[0].Op != GuestOpMeshRejoin || g.cmds[0].Key != "FRESH" {
		t.Fatalf("%v", g.cmds)
	}
	// A repeat of the same task (at-least-once delivery) does nothing.
	m.startRejoin(context.Background(), Task{TaskID: "sbt_r", SandboxID: "sb4", Kind: KindRejoin, SetupKey: "FRESH"})
	m.wg.Wait()
	if len(g.cmds) != 1 {
		t.Fatal("repeat must be ignored")
	}
}

func TestVNCPasswordTask(t *testing.T) {
	g := &fakeGuest{rep: GuestReply{OK: true}}
	m := newTestManager(t, &fakeHyp{}, nil, g)
	put(m, &record{ID: "sb5", State: StateRunning, Kind: SandboxVM, Handle: Handle{Label: "l"}})
	m.startVNC(Task{TaskID: "sbt_v", SandboxID: "sb5", Kind: KindVNCPassword, Password: "abcd1234"})
	m.wg.Wait()
	if len(g.cmds) != 1 || g.cmds[0].Op != GuestOpVNCPassword || g.cmds[0].Password != "abcd1234" {
		t.Fatalf("%v", g.cmds)
	}
	m.startVNC(Task{TaskID: "sbt_v2", SandboxID: "sb5", Kind: KindVNCPassword, Password: "bad pw"})
	m.startVNC(Task{TaskID: "sbt_v3", SandboxID: "nope", Kind: KindVNCPassword, Password: "abcd1234"})
	m.wg.Wait()
	if len(g.cmds) != 1 {
		t.Fatal("invalid password or unknown sandbox must send nothing")
	}
}

func TestStopKeepsDiskAndStartRestarts(t *testing.T) {
	hv := &fakeHyp{alive: map[string]bool{"l1": true}}
	g := &fakeGuest{rep: GuestReply{OK: true, Connected: true, MeshIP: "100.64.0.7"}}
	m := newTestManager(t, hv, nil, g)
	quickStop(m)
	put(m, &record{ID: "sbp", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecyclePersistent,
		Handle: Handle{Label: "l1"}, Boot: &BootInfo{Hostname: "h"}})
	put(m, &record{ID: "sbe", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecycleEphemeral, Handle: Handle{Label: "l2"}})
	if err := os.MkdirAll(m.boxDir("sbp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.diskPath("sbp"), []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop("sbe"); err == nil {
		t.Fatal("ephemeral stop must be refused")
	}
	if err := m.Start("sbp"); err == nil {
		t.Fatal("start of a running sandbox must be refused")
	}
	if err := m.Stop("sbp"); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	r := snap(m, "sbp")
	if r.State != StateStopped || r.Handle.Label != "" || r.busy {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(m.diskPath("sbp")); err != nil {
		t.Fatal("disk must survive a stop")
	}
	if err := m.Start("sbp"); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	r = snap(m, "sbp")
	if r.State != StateRunning || r.MeshIP != "100.64.0.7" || hv.starts != 1 {
		t.Fatalf("%+v starts=%d", r, hv.starts)
	}
}

func TestStopStartRequests(t *testing.T) {
	m := newTestManager(t, &fakeHyp{alive: map[string]bool{"l1": true}}, nil, &fakeGuest{rep: GuestReply{OK: true, Connected: true}})
	quickStop(m)
	put(m, &record{ID: "sbp", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecyclePersistent,
		Handle: Handle{Label: "l1"}, Boot: &BootInfo{Hostname: "h"}})
	put(m, &record{ID: "sbe", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecycleEphemeral})
	_ = os.MkdirAll(m.boxDir("sbp"), 0o700)
	_ = os.WriteFile(m.diskPath("sbp"), []byte("d"), 0o600)
	_ = os.MkdirAll(m.stopRequestsDir(), 0o700)
	for _, id := range []string{"sbp", "sbe"} {
		_ = os.WriteFile(filepath.Join(m.stopRequestsDir(), id), nil, 0o600)
	}
	if n := m.ProcessStopRequests(); n != 1 {
		t.Fatalf("stopped %d, want 1", n)
	}
	m.wg.Wait()
	if _, err := os.Lstat(filepath.Join(m.stopRequestsDir(), "sbe")); err == nil {
		t.Error("refused ephemeral request must be dropped")
	}
	_ = os.MkdirAll(m.startRequestsDir(), 0o700)
	_ = os.WriteFile(filepath.Join(m.startRequestsDir(), "sbp"), nil, 0o600)
	if n := m.ProcessStartRequests(); n != 1 {
		t.Fatalf("started %d, want 1", n)
	}
	m.wg.Wait()
	if snap(m, "sbp").State != StateRunning {
		t.Fatal("not running after start")
	}
}

type rejectStoppedCoord struct{ recCoord }

func (c *rejectStoppedCoord) ReportSandboxState(ctx context.Context, h string, r StateReport) error {
	_ = c.recCoord.ReportSandboxState(ctx, h, r)
	if r.State == StateStopped {
		return os.ErrInvalid // an older coordinator answers 400 invalid_schema
	}
	return nil
}

func TestStopStartReportToCoordinator(t *testing.T) {
	for _, reject := range []bool{false, true} {
		var c Coordinator
		var reps func() []StateReport
		if reject {
			rc := &rejectStoppedCoord{}
			c, reps = rc, rc.all
		} else {
			rc := &recCoord{}
			c, reps = rc, rc.all
		}
		m := newTestManager(t, &fakeHyp{alive: map[string]bool{"l1": true}}, nil, &fakeGuest{rep: GuestReply{OK: true, Connected: true, MeshIP: "100.64.0.7"}})
		withCoord(m, c)
		quickStop(m)
		put(m, &record{ID: "sbp", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecyclePersistent,
			Handle: Handle{Label: "l1"}, Boot: &BootInfo{Hostname: "h"}})
		_ = os.MkdirAll(m.boxDir("sbp"), 0o700)
		_ = os.WriteFile(m.diskPath("sbp"), []byte("d"), 0o600)
		if err := m.Stop("sbp"); err != nil {
			t.Fatal(err)
		}
		m.wg.Wait()
		if snap(m, "sbp").State != StateStopped {
			t.Fatalf("reject=%v: not stopped locally", reject)
		}
		if err := m.Start("sbp"); err != nil {
			t.Fatal(err)
		}
		m.wg.Wait()
		var got []State
		for _, r := range reps() {
			got = append(got, r.State)
		}
		if len(got) != 2 || got[0] != StateStopped || got[1] != StateRunning {
			t.Fatalf("reject=%v: reports %v, want [stopped running]", reject, got)
		}
		if snap(m, "sbp").State != StateRunning {
			t.Fatalf("reject=%v: not running after start", reject)
		}
	}
}

// quickStop shortens the ACPI grace period so a fake VM that never exits is force-killed at once.
func quickStop(m *Manager) {
	m.mu.Lock()
	m.caps.StopGrace = time.Millisecond
	m.mu.Unlock()
}
