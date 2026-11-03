package sandbox

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func logCapture(m *Manager) *bytes.Buffer {
	buf := &bytes.Buffer{}
	m.opts.Logger = slog.New(slog.NewTextHandler(buf, nil))
	return buf
}

func TestSaveLockedWritesSortedRecordsAndDerivedFlags(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	put(m, &record{ID: "b", Hostname: "hb", State: StatePaused, Lifecycle: LifecyclePersistent})
	put(m, &record{ID: "a", Hostname: "ha", State: StateRunning, Lifecycle: LifecycleEphemeral})
	m.mu.Lock()
	m.saveLocked()
	m.mu.Unlock()
	b, err := os.ReadFile(m.statePath())
	if err != nil {
		t.Fatal(err)
	}
	var recs []record
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].ID != "a" || recs[1].ID != "b" {
		t.Fatalf("records not sorted by ID: %+v", recs)
	}
	if recs[0].Name != "ha" || recs[0].Paused || recs[0].Persistent {
		t.Fatalf("a: %+v", recs[0])
	}
	if recs[1].Name != "hb" || !recs[1].Paused || !recs[1].Persistent {
		t.Fatalf("b: %+v", recs[1])
	}
	if _, err := os.Stat(m.statePath() + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestSaveLockedLogsWhenTempFileCannotBeWritten(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	logs := logCapture(m)
	put(m, &record{ID: "a"})
	// A directory at the temp path makes WriteFile fail on every platform.
	if err := os.Mkdir(m.statePath()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.saveLocked()
	m.mu.Unlock()
	if !strings.Contains(logs.String(), "sandbox state not saved") {
		t.Fatalf("expected a warning, got %q", logs.String())
	}
	if _, err := os.Stat(m.statePath()); !os.IsNotExist(err) {
		t.Fatalf("state file must not exist after a failed save: %v", err)
	}
}

func TestSaveLockedLogsWhenRenameFails(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	logs := logCapture(m)
	put(m, &record{ID: "a"})
	// A non-empty directory at the final path makes the rename fail.
	if err := os.MkdirAll(m.statePath()+"/keep", 0o700); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.saveLocked()
	m.mu.Unlock()
	if !strings.Contains(logs.String(), "sandbox state not saved") {
		t.Fatalf("expected a warning, got %q", logs.String())
	}
}
