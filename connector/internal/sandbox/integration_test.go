package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type recCoord struct {
	mu   sync.Mutex
	reps []StateReport
}

func (c *recCoord) SandboxTasks(context.Context, string) ([]Task, error) { return nil, nil }
func (c *recCoord) ReportSandboxState(_ context.Context, _ string, r StateReport) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reps = append(c.reps, r)
	return nil
}
func (c *recCoord) all() []StateReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]StateReport(nil), c.reps...)
}

func withCoord(m *Manager, c Coordinator) {
	m.BindCoordinator(c)
	m.mu.Lock()
	m.hostID = "h1"
	m.mu.Unlock()
}

func TestRejoinReportsAckAndHostKey(t *testing.T) {
	hv := &fakeHyp{alive: map[string]bool{"l1": true}}
	g := &fakeGuest{rep: GuestReply{OK: true, MeshIP: "100.64.0.9"}}
	m := newTestManager(t, hv, nil, g)
	c := &recCoord{}
	withCoord(m, c)
	put(m, &record{ID: "sb4", State: StateRunning, Kind: SandboxVM, Lifecycle: LifecyclePersistent, AwaitKey: true,
		HostKey: "SHA256:fp", HostKeyPub: "ssh-ed25519 AAAA", Handle: Handle{Label: "l1"}, Boot: &BootInfo{Hostname: "h"}})
	m.startRejoin(context.Background(), Task{TaskID: "sbt_r", SandboxID: "sb4", Kind: KindRejoin, SetupKey: "FRESH"})
	m.wg.Wait()
	reps := c.all()
	if len(reps) != 1 || reps[0].AckTaskID != "sbt_r" || reps[0].HostKey != "ssh-ed25519 AAAA" || reps[0].State != StateRunning {
		t.Fatalf("%+v", reps)
	}
	// a redelivered rejoin (already applied) is acknowledged without touching the guest
	m.startRejoin(context.Background(), Task{TaskID: "sbt_r", SandboxID: "sb4", Kind: KindRejoin, SetupKey: "FRESH"})
	m.wg.Wait()
	if len(g.cmds) != 1 {
		t.Fatalf("guest touched twice: %v", g.cmds)
	}
	if reps = c.all(); len(reps) != 2 || reps[1].AckTaskID != "sbt_r" {
		t.Fatalf("%+v", reps)
	}
}

func TestNeedsKeyReportWireShape(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	c := &recCoord{}
	withCoord(m, c)
	put(m, &record{ID: "sb1", State: StateRunning, Kind: SandboxVM})
	m.markAwaitKey("sb1", "gone")
	b, _ := json.Marshal(c.all()[0])
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["needsKey"] != true || got["sandboxId"] != "sb1" {
		t.Fatalf("%s", b)
	}
}

// The Mac app decodes sandboxes/state.json as an array of
// {id,name,state,kind,lifecycle,paused,meshIp,expiresAt,persistent,startedAt}.
func TestStateFileShapeForMacApp(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	m.mu.Lock()
	r := &record{ID: "sb1", Hostname: "sbx-1", Kind: SandboxVM, Lifecycle: LifecyclePersistent, MeshIP: "100.64.0.2", ExpiresAt: &exp}
	m.boxes["sb1"] = r
	for _, to := range []State{StateProvisioning, StateRunning} {
		if err := m.setStateLocked(r, to); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Unlock()
	read := func() map[string]any {
		b, err := os.ReadFile(filepath.Join(m.opts.DataDir, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		var arr []map[string]any
		if err := json.Unmarshal(b, &arr); err != nil || len(arr) != 1 {
			t.Fatalf("not an array of one record: %v %s", err, b)
		}
		return arr[0]
	}
	got := read()
	for k, want := range map[string]any{"id": "sb1", "name": "sbx-1", "state": "running", "kind": "vm",
		"lifecycle": "persistent", "paused": false, "meshIp": "100.64.0.2", "persistent": true} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if _, err := time.Parse(time.RFC3339, got["startedAt"].(string)); err != nil {
		t.Errorf("startedAt: %v", got["startedAt"])
	}
	if _, err := time.Parse(time.RFC3339, got["expiresAt"].(string)); err != nil {
		t.Errorf("expiresAt: %v", got["expiresAt"])
	}
	m.Suspend()
	if g := read(); g["state"] != "paused" || g["paused"] != true {
		t.Errorf("paused: %v", g)
	}
	// the file still round-trips into the manager's own records
	m2, err := NewManager(Options{DataDir: m.opts.DataDir, Images: m.opts.Images, Hypervisor: &fakeHyp{}})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if s := snap(m2, "sb1"); s.Hostname != "sbx-1" || s.StartedAt == nil {
		t.Fatalf("%+v", s)
	}
}

func TestKillRequests(t *testing.T) {
	hv := &fakeHyp{}
	m := newTestManager(t, hv, nil, nil)
	put(m, &record{ID: "sb1", State: StateRunning, Kind: SandboxVM, Handle: Handle{Label: "l1"}})
	put(m, &record{ID: "busy1", State: StateProvisioning, Kind: SandboxVM, busy: true})
	dir := m.killRequestsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sb1", "busy1", "unknown1", "bad name"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "x")
	_ = os.WriteFile(outside, nil, 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if n := m.ProcessKillRequests(); n != 1 {
		t.Fatalf("started %d teardowns, want 1", n)
	}
	m.wg.Wait()
	exists := func(n string) bool { _, err := os.Lstat(filepath.Join(dir, n)); return err == nil }
	if exists("sb1") || exists("unknown1") || exists("bad name") || exists("linked") {
		t.Error("handled requests must be deleted")
	}
	if !exists("busy1") {
		t.Error("a busy sandbox keeps its request for the next poll")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("symlink target must not be touched")
	}
	m.mu.Lock()
	if r, still := m.boxes["sb1"]; still && r.State != StateDeleted {
		t.Errorf("sb1 not torn down: %+v", *r)
	}
	m.mu.Unlock()
	// stale busy requests expire
	old := time.Now().Add(-2 * killRequestTTL)
	_ = os.Chtimes(filepath.Join(dir, "busy1"), old, old)
	m.ProcessKillRequests()
	if exists("busy1") {
		t.Error("expired request must be dropped")
	}
}
