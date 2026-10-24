package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scriptedDevPod returns a DevPod whose docker calls are answered by reply.
func scriptedDevPod(t *testing.T, reply func(line string) ([]byte, error)) (*DevPod, devTools) {
	t.Helper()
	env := fakeEnv(&fakeRun{}, "/h/.colima/default/docker.sock")
	env.Run = func(ctx context.Context, _ []string, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		if strings.Contains(line, "version --format") {
			return []byte("27.0"), nil
		}
		return reply(line)
	}
	d := NewDevPod(DevConfig{Env: env, StateDir: t.TempDir(), MeshImage: "m", Sleep: func(time.Duration) {}})
	tl, err := d.tools(context.Background(), "ws1", false)
	if err != nil {
		t.Fatal(err)
	}
	return d, tl
}

func TestContainerIDFallsBackToWorkspaceUID(t *testing.T) {
	d, tl := scriptedDevPod(t, func(line string) ([]byte, error) {
		if strings.Contains(line, "ps -q ") {
			return nil, nil // no container labelled with the workspace id
		}
		return []byte("aaa\trunning\tother=1," + workspaceLabel + "=someone-else\n" +
			"bbb\texited\t" + workspaceLabel + "=uid-9\n" +
			"ccc\trunning\tx=y," + workspaceLabel + "=uid-9\n"), nil
	})
	wj := filepath.Join(d.wsDir("ws1"), "devpod", "contexts", "default", "workspaces", "ws1")
	if err := os.MkdirAll(wj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wj, "workspace.json"), []byte(`{"uid":"uid-9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := d.containerID(context.Background(), tl, "ws1", true); err != nil || got != "ccc" {
		t.Fatalf("running only: %q %v", got, err)
	}
	if got, err := d.containerID(context.Background(), tl, "ws1", false); err != nil || got != "bbb" {
		t.Fatalf("any state: %q %v", got, err)
	}
}

func TestContainerIDNoneAndErrors(t *testing.T) {
	d, tl := scriptedDevPod(t, func(line string) ([]byte, error) {
		if strings.Contains(line, "ps -q ") {
			return nil, nil
		}
		return []byte("aaa\trunning\tfoo=bar\n\nbad line\n"), nil
	})
	if got, err := d.containerID(context.Background(), tl, "ws1", false); err != nil || got != "" {
		t.Fatalf("no match: %q %v", got, err)
	}
	d, tl = scriptedDevPod(t, func(string) ([]byte, error) { return []byte("boom"), errors.New("exit 1") })
	if _, err := d.containerID(context.Background(), tl, "ws1", false); err == nil {
		t.Fatal("docker failure must surface")
	}
}

func TestContainerSnapshot(t *testing.T) {
	cases := []struct {
		out  string
		err  error
		want string
	}{
		{"c1 running img lbl\n", nil, "c1 running img lbl"},
		{" \n", nil, "no containers on "},
		{"denied", errors.New("exit 1"), "docker ps failed: "},
	}
	for _, c := range cases {
		d, tl := scriptedDevPod(t, func(string) ([]byte, error) { return []byte(c.out), c.err })
		if got := d.containerSnapshot(context.Background(), tl); !strings.HasPrefix(got, c.want) {
			t.Errorf("snapshot(%q) = %q, want prefix %q", c.out, got, c.want)
		}
	}
}
