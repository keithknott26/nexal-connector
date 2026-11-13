package remoteinference

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestStartProcReportsStartFailure(t *testing.T) {
	r := &Runner{Exe: filepath.Join(t.TempDir(), "missing-nexal"), LogDir: filepath.Join(t.TempDir(), "logs")}
	p, err := r.startProc(context.Background(), "pool-serve", "big")
	if err == nil || p != nil {
		t.Fatalf("startProc = %v, %v; want nil proc and an error", p, err)
	}
}

func TestStartProcCapturesOutputLogsAndReportsExit(t *testing.T) {
	exe := script(t, `echo "helper says hi"; echo "boom" >&2; exit 3`)
	logDir := filepath.Join(t.TempDir(), "logs")
	r := &Runner{Exe: exe, LogDir: logDir}
	p, err := r.startProc(context.Background(), "pool-serve", "big")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit")
	}
	if p.alive() {
		t.Fatal("alive() true after the helper exited")
	}
	got := p.exit()
	if !strings.Contains(got, "helper says hi") || !strings.Contains(got, "boom") || !strings.Contains(got, "exit status 3") {
		t.Fatalf("exit() = %q", got)
	}
	data, err := os.ReadFile(filepath.Join(logDir, "inference-pool-big.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"=== ", "pool-serve big", "helper says hi", "exited:"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("log missing %q:\n%s", want, data)
		}
	}
}

func TestStartProcStopEndsALongRunningHelper(t *testing.T) {
	exe := script(t, `exec sleep 60`)
	r := &Runner{Exe: exe} // no LogDir: output goes only to the tail
	p, err := r.startProc(context.Background(), "pool-serve", "big")
	if err != nil {
		t.Fatal(err)
	}
	if !p.alive() {
		t.Fatal("helper should be running")
	}
	p.stop()
	if p.alive() {
		t.Fatal("helper still running after stop")
	}
}
