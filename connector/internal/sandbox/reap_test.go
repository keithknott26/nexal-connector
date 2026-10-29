package sandbox

import (
	"os"
	"testing"
	"time"
)

// A box whose cleanup failed earlier is retried by reap and forgotten once its
// files are gone; a busy box is left alone.
func TestReapRetriesPendingCleanupAndSkipsBusy(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, &fakeDev{}, nil)
	for _, id := range []string{"gone", "busy"} {
		if err := os.MkdirAll(m.boxDir(id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	put(m, &record{ID: "gone", State: StateDeleted, CleanupPending: true})
	put(m, &record{ID: "busy", State: StateDeleted, CleanupPending: true, busy: true})

	m.reap()

	m.mu.Lock()
	_, goneKept := m.boxes["gone"]
	_, busyKept := m.boxes["busy"]
	m.mu.Unlock()
	if goneKept {
		t.Fatal("cleaned-up box should be dropped from the table")
	}
	if !busyKept {
		t.Fatal("busy box must not be reaped")
	}
	if _, err := os.Stat(m.boxDir("busy")); err != nil {
		t.Fatalf("busy box files touched: %v", err)
	}
}

// Expiry applies only to non-persistent boxes: an expired persistent box is
// not torn down.
func TestReapIgnoresExpiredPersistentBox(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, &fakeDev{}, nil)
	past := time.Now().Add(-time.Hour)
	put(m, &record{ID: "keep", State: StateRunning, Lifecycle: LifecyclePersistent, ExpiresAt: &past})

	m.reap()
	m.wg.Wait()

	if got := snap(m, "keep"); got.State != StateRunning {
		t.Fatalf("persistent box state = %q, want running", got.State)
	}
}
