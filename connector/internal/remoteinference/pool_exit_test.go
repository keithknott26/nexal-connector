package remoteinference

import (
	"errors"
	"strings"
	"testing"
)

func TestPoolExitNamesTheErrorAndTheServerOutput(t *testing.T) {
	got := poolExit("loading model\nout of memory", errors.New("exit status 1"))
	if !strings.HasPrefix(got, "exit status 1. Server output: ") || !strings.Contains(got, "out of memory") {
		t.Fatalf("poolExit = %q", got)
	}
}

func TestPoolExitSaysTheServerExitedWithoutAnError(t *testing.T) {
	if got := poolExit("", nil); !strings.HasPrefix(got, "the server exited. Server output:") {
		t.Fatalf("poolExit = %q", got)
	}
}

func TestPoolExitKeepsTheEndOfALongMessage(t *testing.T) {
	got := poolExit(strings.Repeat("x", 1000)+"THE-END", errors.New("boom"))
	if r := []rune(got); len(r) != 280 || !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, "THE-END") {
		t.Fatalf("poolExit len=%d %q", len([]rune(got)), got)
	}
}

func TestRunnerCurrentReportsOnlyRequestsInTheLatestPoll(t *testing.T) {
	r := &Runner{latest: map[string]Task{"a": {ID: "a"}}}
	if tk, ok := r.current("a"); !ok || tk.ID != "a" {
		t.Fatalf("current(a) = %+v, %v", tk, ok)
	}
	if _, ok := r.current("gone"); ok {
		t.Fatal("a request missing from the latest poll must read as cancelled")
	}
}
