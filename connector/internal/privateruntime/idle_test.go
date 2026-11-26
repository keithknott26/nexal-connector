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

// scriptedAPI answers OpenRuntime from a fixed list (nil delegates to the fake
// session); an exhausted script returns "unassigned" so the loop keeps idling.
type scriptedAPI struct {
	fake
	script []error
}

func (s *scriptedAPI) OpenRuntime(ctx context.Context, host string) (Session, error) {
	if len(s.script) == 0 {
		return Session{}, coded{"runtime_unassigned"}
	}
	next := s.script[0]
	s.script = s.script[1:]
	if next != nil {
		return Session{}, next
	}
	return s.fake.OpenRuntime(ctx, host)
}

// stopOnRecovery records messages and cancels the loop once recovery is logged.
type stopOnRecovery struct {
	slog.Handler
	cancel context.CancelFunc
	msgs   *[]string
}

func (h stopOnRecovery) Handle(ctx context.Context, r slog.Record) error {
	*h.msgs = append(*h.msgs, r.Message)
	if strings.Contains(r.Message, "available again") {
		h.cancel()
	}
	return nil
}

func TestRunLoopLogsIdleOnceThenRecovery(t *testing.T) {
	f, m := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.API = &scriptedAPI{fake: *f, script: []error{coded{"runtime_unassigned"}, coded{"runtime_unassigned"}, nil}}
	var msgs []string
	m.IdleRetry = time.Millisecond
	m.Run = func(context.Context, string, string) error { return nil }
	m.Logger = slog.New(stopOnRecovery{Handler: slog.NewTextHandler(io.Discard, nil), cancel: cancel, msgs: &msgs})
	done := make(chan error, 1)
	go func() { done <- m.RunLoop(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLoop = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunLoop did not stop after recovery was logged")
	}
	if len(msgs) != 2 || !strings.Contains(msgs[0], "no private runtime assigned") || !strings.Contains(msgs[1], "available again") {
		t.Fatalf("log messages = %q, want one idle notice then one recovery", msgs)
	}
}
