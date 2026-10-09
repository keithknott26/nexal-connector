package diaglog

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDedupCollapsesIdenticalLines(t *testing.T) {
	var out bytes.Buffer
	d := NewDedup(&out)
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		d.Write([]byte(`{"time":"t` + string(rune('a'+i)) + `","msg":"x"}` + "\n"))
		now = now.Add(10 * time.Second)
	}
	d.Write([]byte(`{"time":"t","msg":"y"}` + "\n"))
	want := "{\"time\":\"ta\",\"msg\":\"x\"}\n(repeated 3 times in 30s)\n{\"time\":\"t\",\"msg\":\"y\"}\n"
	if out.String() != want {
		t.Fatalf("got %q want %q", out.String(), want)
	}
}

func TestDedupMaxHoldAndFlush(t *testing.T) {
	var out bytes.Buffer
	d := NewDedup(&out)
	now := time.Unix(0, 0)
	d.now = func() time.Time { return now }
	d.Write([]byte("a\n"))
	now = now.Add(time.Minute)
	d.Write([]byte("a\n"))
	now = now.Add(DedupMaxHold)
	d.Write([]byte("a\n"))
	if got := out.String(); got != "a\n(repeated 1 times in 1m0s)\na\n" {
		t.Fatalf("got %q", got)
	}
	out.Reset()
	d.Write([]byte("a\n"))
	d.Flush()
	if !strings.Contains(out.String(), "repeated 1 times") {
		t.Fatalf("flush: %q", out.String())
	}
}

func TestControllerFlushWritesPendingRepeat(t *testing.T) {
	var out bytes.Buffer
	c := New(&out, slog.LevelInfo, "", "")
	l := c.Logger()
	for i := 0; i < 3; i++ {
		l.Warn("same")
	}
	if strings.Contains(out.String(), "repeated") {
		t.Fatalf("summary before flush: %q", out.String())
	}
	c.Flush()
	if !strings.Contains(out.String(), "(repeated 2 times in") {
		t.Fatalf("flush lost summary: %q", out.String())
	}
}
