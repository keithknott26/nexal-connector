package cybersecurity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanaryLockedRejectsUnsafeDirectoryAndBadState(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, dir string)
		want  string
	}{
		"group-readable directory": {func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "security-canary"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, "security-canary"), 0755); err != nil {
				t.Fatal(err)
			}
		}, "unsafe canary directory"},
		"corrupt state": {func(t *testing.T, dir string) {
			writeCanaryState(t, dir, []byte("{not json"))
		}, "invalid canary state"},
		"oversized state": {func(t *testing.T, dir string) {
			writeCanaryState(t, dir, []byte(`{"status":"`+strings.Repeat("a", 33000)+`"}`))
		}, "invalid canary state"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			if _, err := (Canary{Directory: dir}).Status(); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCanaryLockedFailsOnMissingParent(t *testing.T) {
	if _, err := (Canary{Directory: filepath.Join(t.TempDir(), "missing")}).Status(); err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

func writeCanaryState(t *testing.T, dir string, b []byte) {
	t.Helper()
	d := filepath.Join(dir, "security-canary")
	if err := os.Mkdir(d, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
