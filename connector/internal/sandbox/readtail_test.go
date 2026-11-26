package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTailReturnsOnlyTrailingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.log")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		max  int64
		want string
	}{
		{4, "6789"},
		{10, "0123456789"},
		{100, "0123456789"},
	} {
		got, err := readTail(path, c.max)
		if err != nil || got != c.want {
			t.Fatalf("readTail(max=%d) = %q, %v; want %q", c.max, got, err, c.want)
		}
	}
}

func TestReadTailMissingFileErrors(t *testing.T) {
	if _, err := readTail(filepath.Join(t.TempDir(), "absent.log"), 8); err == nil {
		t.Fatal("readTail of a missing file returned no error")
	}
}

func TestConsoleSnippet(t *testing.T) {
	if got := consoleSnippet(""); got != "<none>" {
		t.Fatalf("empty console = %q, want <none>", got)
	}
	if got := consoleSnippet("\n  \n\n"); got != "<none>" {
		t.Fatalf("blank-only console = %q, want <none>", got)
	}
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("  line%d  ", i), "")
	}
	got := consoleSnippet(strings.Join(lines, "\n"))
	if want := "line3 | line4 | line5 | line6 | line7 | line8 | line9 | line10"; got != want {
		t.Fatalf("snippet keeps last 8 non-blank trimmed lines: got %q, want %q", got, want)
	}
	long := consoleSnippet(strings.Repeat("x", 5000))
	if len(long) != 2003 || !strings.HasPrefix(long, "...") || strings.Trim(long[3:], "x") != "" {
		t.Fatalf("long snippet not truncated to a 2000-byte tail: len=%d", len(long))
	}
}
