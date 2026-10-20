package sandbox

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestTailOutput(t *testing.T) {
	if got := tailOutput("  short\n", 100); got != "short" {
		t.Fatalf("short input should be trimmed only, got %q", got)
	}
	got := tailOutput("aaaaaaaaaa-last-lines", 10)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "lines") {
		t.Fatalf("expected ellipsis + tail, got %q", got)
	}
	// A cut through a multibyte rune must not leave invalid UTF-8.
	got = tailOutput("xx日本語", 4)
	if !strings.HasPrefix(got, "…") || strings.ToValidUTF8(got, "") != got {
		t.Fatalf("invalid utf-8 in %q", got)
	}
}

func TestRefusedErrorMessage(t *testing.T) {
	err := refuse("too_big", "need %d cpus", 4)
	if got, want := err.Error(), "sandbox refused (too_big): need 4 cpus"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTaskLogValueAndString(t *testing.T) {
	task := Task{TaskID: "t1", SandboxID: "s1", Kind: KindCreate}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "task", task)
	out := buf.String()
	for _, want := range []string{"task.task=t1", "task.sandbox=s1", "task.kind=create"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output %q missing %q", out, want)
		}
	}
	if s := task.String(); !strings.Contains(s, "task=t1") || !strings.Contains(s, "sandbox=s1") {
		t.Fatalf("String() = %q", s)
	}
}
