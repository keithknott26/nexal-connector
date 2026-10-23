package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSystemDevEnvWiresRealHost(t *testing.T) {
	env := SystemDevEnv()
	if env.GOOS != runtime.GOOS {
		t.Fatalf("GOOS = %q, want %q", env.GOOS, runtime.GOOS)
	}
	if home, _ := os.UserHomeDir(); env.Home != home {
		t.Fatalf("Home = %q, want %q", env.Home, home)
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !env.Exists(file) || env.Exists(filepath.Join(dir, "missing")) {
		t.Fatal("Exists does not reflect the filesystem")
	}
	if got := env.Glob(filepath.Join(dir, "*.txt")); len(got) != 1 || got[0] != file {
		t.Fatalf("Glob = %v, want [%s]", got, file)
	}
	if got := env.Glob(filepath.Join(dir, "[")); len(got) != 0 {
		t.Fatalf("Glob with a bad pattern = %v, want none", got)
	}

	if _, err := env.LookPath("nexal-no-such-binary"); err == nil {
		t.Fatal("LookPath found a binary that does not exist")
	}
	if runtime.GOOS != "windows" {
		out, err := env.Run(context.Background(), []string{"NEXAL_TEST_VAR=ok"}, "sh", "-c", "printf %s \"$NEXAL_TEST_VAR\"")
		if err != nil || string(out) != "ok" {
			t.Fatalf("Run = %q, %v; want the extra env passed through", out, err)
		}
	}
}

func TestManagerNudgeCoalesces(t *testing.T) {
	m := &Manager{nudgeCh: make(chan struct{}, 1)}
	m.signalNudge()
	m.signalNudge() // buffer full: must not block
	select {
	case <-m.Nudge():
	default:
		t.Fatal("no nudge delivered")
	}
	select {
	case <-m.Nudge():
		t.Fatal("second nudge should have been dropped")
	default:
	}
}
