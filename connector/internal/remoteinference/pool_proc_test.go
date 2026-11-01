package remoteinference

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type countingCloser struct{ n int }

func (c *countingCloser) Close() error { c.n++; return nil }

func TestProcExitReportsErrorAndTailedOutput(t *testing.T) {
	tail := &lineTail{}
	_, _ = tail.Write([]byte("loading model\nout of memory\n"))
	p := &proc{tail: tail, err: errors.New("exit status 1")}
	got := p.exit()
	if !strings.Contains(got, "exit status 1") || !strings.Contains(got, "out of memory") {
		t.Fatalf("exit() = %q", got)
	}
}

func TestProcStopNilIsNoop(t *testing.T) {
	var p *proc
	p.stop() // must not panic
}

func TestProcStopCancelsWaitsAndClosesRingOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	relay := &countingCloser{}
	p := &proc{cancel: cancel, done: done, ring: &ringPlan{relays: []io.Closer{relay}}}
	go func() { <-ctx.Done(); close(done) }()
	p.stop()
	p.stop() // a second stop must not close the relays again
	if ctx.Err() == nil {
		t.Fatal("stop did not cancel the helper context")
	}
	if relay.n != 1 {
		t.Fatalf("ring relays closed %d times, want 1", relay.n)
	}
}
