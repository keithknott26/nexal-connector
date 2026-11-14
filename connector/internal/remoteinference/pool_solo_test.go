package remoteinference

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func soloLeader(exe string, coord *fakeCoord) *leader {
	return &leader{
		r: &Runner{
			Exe: exe, Coord: coord, ReadyAfter: time.Minute,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		t: Task{ID: "task-1", ModelID: "big"},
	}
}

func TestStartSoloReportsFailureWhenHelperCannotStart(t *testing.T) {
	coord := &fakeCoord{}
	l := soloLeader(filepath.Join(t.TempDir(), "missing-nexal"), coord)
	if l.startSolo(context.Background(), "why") {
		t.Fatal("startSolo succeeded with a missing executable")
	}
	if got := coord.last(); got.State != "failed" || got.Step != "serve" {
		t.Fatalf("last report = %+v, want failed/serve", got)
	}
	if l.solo != nil {
		t.Fatal("solo must stay unset after a failed start")
	}
}

func TestStartSoloReportsFailureWhenHelperExitsEarly(t *testing.T) {
	coord := &fakeCoord{}
	l := soloLeader(script(t, `echo "no metal device" >&2; exit 2`), coord)
	if l.startSolo(context.Background(), "why") {
		t.Fatal("startSolo succeeded though the helper exited")
	}
	got := coord.last()
	if got.State != "failed" || got.Step != "serve" {
		t.Fatalf("last report = %+v, want failed/serve", got)
	}
	if l.solo != nil {
		t.Fatal("solo must stay unset after the helper exited")
	}
}

func TestStartSoloStopsHelperWhenContextCancelled(t *testing.T) {
	coord := &fakeCoord{}
	l := soloLeader(script(t, `exec sleep 60`), coord)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	if l.startSolo(ctx, "why") {
		t.Fatal("startSolo succeeded after cancellation")
	}
	if l.solo != nil {
		t.Fatal("solo must stay unset after cancellation")
	}
}
