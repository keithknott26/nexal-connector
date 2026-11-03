package sandbox

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// dispatch must drop invalid tasks and log (not act on) refused remote stop/start requests.
func TestDispatchRejectsInvalidAndLogsRefusedStopStart(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	var buf bytes.Buffer
	m.opts.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m.dispatch(context.Background(), Task{TaskID: "bad id!", SandboxID: "s1", Kind: KindStop})
	m.dispatch(context.Background(), Task{TaskID: "t1", SandboxID: "s1", Kind: Kind("explode")})
	if got := buf.String(); strings.Count(got, "sandbox task rejected") != 2 || strings.Contains(got, "sandbox task received") {
		t.Fatalf("invalid tasks should only be rejected:\n%s", got)
	}

	buf.Reset()
	m.dispatch(context.Background(), Task{TaskID: "t2", SandboxID: "unknown", Kind: KindStop})
	m.dispatch(context.Background(), Task{TaskID: "t3", SandboxID: "unknown", Kind: KindStart})
	got := buf.String()
	if !strings.Contains(got, "remote stop refused") || !strings.Contains(got, "remote start refused") {
		t.Fatalf("unknown sandbox stop/start should be logged as refused:\n%s", got)
	}
	if n := len(m.boxes); n != 0 {
		t.Fatalf("refused tasks created %d records", n)
	}
}
