package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSidecarLogTail(t *testing.T) {
	if got := sidecarLogTail("\n  \nNEXAL-FIRSTBOOT FAILED\n", 5); got != "(empty)" {
		t.Fatalf("empty input = %q", got)
	}
	// Routine lines are dropped in favour of the ones that explain the failure.
	out := "starting\nNEXAL-FIRSTBOOT begin\nINFO ready\ndial tcp: no such host\nTLS x509 bad cert\n"
	if got, want := sidecarLogTail(out, 5), "dial tcp: no such host | TLS x509 bad cert"; got != want {
		t.Fatalf("notable lines = %q, want %q", got, want)
	}
	// With nothing notable, the newest n lines are kept.
	if got, want := sidecarLogTail("a\nb\nc\n", 2), "b | c"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
	// The result stays bounded.
	long := strings.Repeat("error "+strings.Repeat("x", 100)+"\n", 20)
	if got := sidecarLogTail(long, 20); len(got) > 900+len("…") {
		t.Fatalf("unbounded tail: %d bytes", len(got))
	}
}

func TestWorkspaceUID(t *testing.T) {
	d := &DevPod{cfg: DevConfig{StateDir: t.TempDir()}}
	if got := d.workspaceUID("ws1"); got != "" {
		t.Fatalf("missing file = %q, want empty", got)
	}
	dir := filepath.Join(d.wsDir("ws1"), "devpod", "contexts", "default", "workspaces", "ws1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "workspace.json")
	if err := os.WriteFile(f, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := d.workspaceUID("ws1"); got != "" {
		t.Fatalf("bad json = %q, want empty", got)
	}
	if err := os.WriteFile(f, []byte(`{"uid":"abc123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := d.workspaceUID("ws1"); got != "abc123" {
		t.Fatalf("uid = %q", got)
	}
}

func TestSetAppLast(t *testing.T) {
	m := &Manager{}
	m.setAppLast("a", nil) // forgetting on a nil map must not panic
	m.setAppLast("a", &AppProgress{Step: "app-packages", Percent: 10})
	if got := m.appLast["a"]; got.Step != "app-packages" || got.Percent != 10 {
		t.Fatalf("appLast = %+v", got)
	}
	m.setAppLast("a", nil)
	if _, ok := m.appLast["a"]; ok {
		t.Fatal("entry not forgotten")
	}
}
