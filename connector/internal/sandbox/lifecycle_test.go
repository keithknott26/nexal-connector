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
