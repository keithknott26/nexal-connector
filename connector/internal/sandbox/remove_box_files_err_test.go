package sandbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
)

type failingDeleteDev struct {
	fakeDev
	err error
}

func (f *failingDeleteDev) Delete(ctx context.Context, ws string) error {
	_ = f.fakeDev.Delete(ctx, ws)
	return f.err
}

// A failed workspace delete is reported to the caller (so the reaper retries),
// but the box's own files are still removed.
func TestRemoveBoxFilesErrReportsDevDeleteFailureButStillRemovesFiles(t *testing.T) {
	want := errors.New("devpod delete failed")
	dev := &failingDeleteDev{err: want}
	m := newTestManager(t, &fakeHyp{}, dev, nil)
	if err := os.MkdirAll(m.boxDir("sb1"), 0o700); err != nil {
		t.Fatal(err)
	}
	put(m, &record{ID: "sb1", Workspace: "ws1"})

	if err := m.removeBoxFilesErr("sb1"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want the dev delete error", err)
	}
	if _, err := os.Stat(m.boxDir("sb1")); !os.IsNotExist(err) {
		t.Fatalf("box dir should be gone even when the dev delete failed: %v", err)
	}
	if len(dev.deleted) != 1 || dev.deleted[0] != "ws1" {
		t.Fatalf("dev delete not attempted: %v", dev.deleted)
	}
}

// The best-effort wrapper logs the failure instead of returning it, and still
// removes the box's files.
func TestRemoveBoxFilesLogsFailureAndRemovesFiles(t *testing.T) {
	var buf bytes.Buffer
	dev := &failingDeleteDev{err: errors.New("devpod delete failed")}
	m := newTestManager(t, &fakeHyp{}, dev, nil)
	m.opts.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	if err := os.MkdirAll(m.boxDir("sb1"), 0o700); err != nil {
		t.Fatal(err)
	}
	put(m, &record{ID: "sb1", Workspace: "ws1"})

	m.removeBoxFiles("sb1")

	if !strings.Contains(buf.String(), "sandbox files not removed") || !strings.Contains(buf.String(), "devpod delete failed") {
		t.Fatalf("failure not logged: %q", buf.String())
	}
	if _, err := os.Stat(m.boxDir("sb1")); !os.IsNotExist(err) {
		t.Fatalf("box dir should be gone: %v", err)
	}

	buf.Reset()
	m.removeBoxFiles("../escape")
	if !strings.Contains(buf.String(), "invalid sandbox id") {
		t.Fatalf("invalid id not logged: %q", buf.String())
	}
}
