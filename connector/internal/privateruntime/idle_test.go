package privateruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type coded struct{ code string }

func (c coded) Error() string     { return "coordinator rejected request (HTTP 503)" }
func (c coded) ErrorCode() string { return c.code }

type unassignedAPI struct{ opens atomic.Int32 }

func (a *unassignedAPI) OpenRuntime(context.Context, string) (Session, error) {
	a.opens.Add(1)
	return Session{}, coded{"runtime_unassigned"}
}
func (a *unassignedAPI) RenewRuntime(context.Context, string, string) (Lease, error) {
	return Lease{}, errors.New("unused")
}
func (a *unassignedAPI) DownloadRuntime(context.Context, string, string, int, io.Writer, int64) error {
	return errors.New("unused")
}
func (a *unassignedAPI) CloseRuntime(context.Context, string, string) error { return nil }

func TestIdleClassifiesCoordinatorAnswers(t *testing.T) {
	for _, c := range []struct {
		err     error
		waiting bool
		reason  string
	}{
		{nil, false, ""},
		{coded{"runtime_unassigned"}, true, "runtime_unassigned"},
		{coded{"runtime_storage_unconfigured"}, true, "runtime_storage_unconfigured"},
		{coded{"runtime_manifest_invalid"}, false, "runtime_manifest_invalid"},
		{errors.New("dial tcp: timeout"), false, "dial tcp: timeout"},
	} {
		if w, r := idle(c.err); w != c.waiting || r != c.reason {
			t.Errorf("idle(%v) = %v, %q; want %v, %q", c.err, w, r, c.waiting, c.reason)
		}
	}
}

// With nothing assigned, the loop asks once, logs once, and waits IdleRetry instead of 30 s.
func TestUnassignedRuntimeBacksOffAndLogsOnce(t *testing.T) {
	api := &unassignedAPI{}
	var logs bytes.Buffer
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	m := Manager{API: api, HostID: "host_abc", Root: root, Connected: func() bool { return true },
		IdleRetry: time.Hour, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := m.RunLoop(ctx); err != nil {
		t.Fatal(err)
	}
	if n := api.opens.Load(); n != 1 {
		t.Fatalf("OpenRuntime called %d times, want 1 (then wait IdleRetry)", n)
	}
	if got := strings.Count(logs.String(), "no private runtime assigned"); got != 1 {
		t.Fatalf("logged %d times, want once:\n%s", got, logs.String())
	}
}
