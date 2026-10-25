package remoteinference

import (
	"errors"
	"testing"
)

func TestLastLineFallsBackToTheErrorWhenOutputIsBlank(t *testing.T) {
	boom := errors.New("exit status 1")
	for _, tc := range []struct {
		name, out, want string
	}{
		{"last line wins", "one\n two \n", "two"},
		{"single line", "only", "only"},
		{"empty output uses error", "", "exit status 1"},
		{"whitespace output uses error", " \n\t\n", "exit status 1"},
	} {
		if got := lastLine(tc.out, boom); got != tc.want {
			t.Errorf("%s: lastLine = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLastLinesKeepsTheTailOfNonEmptyLines(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		n         int
		want      string
	}{
		{"empty is none", "", 3, "none"},
		{"blank lines only is none", "\n  \n\t\n", 3, "none"},
		{"fewer than n keeps all", "a\nb", 3, "a | b"},
		{"more than n keeps the end", "a\nb\nc\nd", 2, "c | d"},
		{"blank lines are skipped and trimmed", "a\n\n  b  \n\nc\n", 5, "a | b | c"},
	} {
		if got := lastLines(tc.out, tc.n); got != tc.want {
			t.Errorf("%s: lastLines = %q, want %q", tc.name, got, tc.want)
		}
	}
}
