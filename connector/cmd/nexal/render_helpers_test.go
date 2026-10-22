package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/inference"
)

func TestTransportLogLinesDropsBlankAndCapsGrowth(t *testing.T) {
	l := &transportLog{}
	if _, err := l.Write([]byte("first\n\n  \nsecond\n")); err != nil {
		t.Fatal(err)
	}
	got := l.lines()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("lines = %q, want [first second]", got)
	}

	// Once the buffer is past the cap, writes are accepted but not stored.
	big := bytes.Repeat([]byte("x"), 9<<10)
	if n, err := l.Write(big); err != nil || n != len(big) {
		t.Fatalf("write big = %d, %v", n, err)
	}
	before := l.buf.Len()
	if n, err := l.Write([]byte("dropped\n")); err != nil || n != len("dropped\n") {
		t.Fatalf("write past cap = %d, %v", n, err)
	}
	if l.buf.Len() != before {
		t.Fatalf("buffer grew past cap: %d -> %d", before, l.buf.Len())
	}
}

func TestPlainInstallRenderProgressSteps(t *testing.T) {
	var out bytes.Buffer
	p := &plainInstall{w: &out}
	prog := func(b, total int64) inference.ProgressEvent {
		return inference.ProgressEvent{File: "m.gguf", Bytes: b, Total: total, OverallBytes: b, OverallTotal: total}
	}
	p.render(prog(5, 100))  // step 0, first sighting: printed
	p.render(prog(8, 100))  // still step 0: suppressed
	p.render(prog(10, 100)) // step 1: printed
	p.render(prog(50, 0))   // unknown total: ignored
	p.render(prog(100, 100))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], "5%") || !strings.Contains(lines[1], "10%") || !strings.Contains(lines[2], "100%") {
		t.Fatalf("unexpected progress lines:\n%s", out.String())
	}
}

func TestPlainInstallRenderErrorShowsFixOnlyWhenPresent(t *testing.T) {
	var out bytes.Buffer
	p := &plainInstall{w: &out}
	p.render(inference.ErrorEvent{Code: "disk_full", Message: "no space"})
	if strings.Contains(out.String(), "Fix:") {
		t.Fatalf("unexpected Fix line: %q", out.String())
	}
	p.render(inference.ErrorEvent{Code: "disk_full", Message: "no space", Fix: "free space"})
	if !strings.Contains(out.String(), "Install failed [disk_full]: no space") || !strings.Contains(out.String(), "Fix: free space") {
		t.Fatalf("missing error/fix lines: %q", out.String())
	}
}

func TestSameConfigFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Link(a, link); err != nil {
		t.Skipf("hard links unsupported: %v", err)
	}
	if !sameConfigFile(a, link) {
		t.Fatal("hard link should be the same file")
	}
	if sameConfigFile(a, b) {
		t.Fatal("distinct files reported as same")
	}
	if sameConfigFile(a, filepath.Join(dir, "missing.json")) || sameConfigFile(filepath.Join(dir, "missing.json"), a) {
		t.Fatal("missing file must not compare equal")
	}
}
