package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoxPathHelpers(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	box := filepath.Join(m.opts.DataDir, "boxes", "sb1")
	for got, want := range map[string]string{
		m.boxDir("sb1"):     box,
		m.seedDir("sb1"):    filepath.Join(box, "seed"),
		m.seedISO("sb1"):    filepath.Join(box, "seed.iso"),
		m.consoleLog("sb1"): filepath.Join(box, "console.log"),
	} {
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestFailReportsWithoutTouchingRecords(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	c := &recCoord{}
	withCoord(m, c)
	put(m, &record{ID: "keep", State: StateRunning})
	m.fail("keep", "no capacity")
	reps := c.all()
	if len(reps) != 1 || reps[0].SandboxID != "keep" || reps[0].State != StateFailed || reps[0].Error != "no capacity" {
		t.Fatalf("reports %+v", reps)
	}
	if snap(m, "keep").State != StateRunning {
		t.Fatal("fail must leave the record untouched")
	}
}

func TestFailRecordKillsRemovesFilesAndKeepsRecord(t *testing.T) {
	hv := &fakeHyp{alive: map[string]bool{"l1": true}}
	m := newTestManager(t, hv, nil, nil)
	c := &recCoord{}
	withCoord(m, c)
	put(m, &record{ID: "sb1", State: StateRunning, Kind: SandboxVM, Handle: Handle{Label: "l1"}})
	if err := os.MkdirAll(m.boxDir("sb1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.diskPath("sb1"), []byte("d"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.failRecord("sb1", "boom")
	if _, err := os.Stat(m.diskPath("sb1")); !os.IsNotExist(err) {
		t.Fatalf("disk should be removed, stat err=%v", err)
	}
	r := snap(m, "sb1")
	if r.State != StateFailed || r.Handle != (Handle{}) {
		t.Fatalf("record %+v", r)
	}
	if len(hv.killed) != 1 || hv.killed[0] != "l1" {
		t.Fatalf("killed %v", hv.killed)
	}
	reps := c.all()
	if len(reps) != 1 || reps[0].State != StateFailed || reps[0].Error != "boom" {
		t.Fatalf("reports %+v", reps)
	}
}

func TestFailRecordUnknownSandboxStillReports(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	c := &recCoord{}
	withCoord(m, c)
	m.failRecord("ghost", "gone")
	if reps := c.all(); len(reps) != 1 || reps[0].SandboxID != "ghost" {
		t.Fatalf("reports %+v", reps)
	}
}
