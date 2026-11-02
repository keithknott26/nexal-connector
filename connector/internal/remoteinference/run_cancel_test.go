package remoteinference

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Run must return promptly when its context is already cancelled, before the
// first poll is attempted.
func TestRunReturnsOnCancelBeforeFirstPoll(t *testing.T) {
	c := &fakeCoord{}
	r := &Runner{Coord: c, HostID: "h", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
	if len(r.serving) != 0 {
		t.Fatalf("serving not empty after Run: %v", r.serving)
	}
	if r.Poll == 0 || r.MaxPort == 0 {
		t.Fatal("Run did not apply defaults via init")
	}
}
