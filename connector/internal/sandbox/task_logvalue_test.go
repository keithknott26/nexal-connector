package sandbox

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestTaskLogValueRedactsSecrets asserts a Task passed to slog emits only its
// identifying fields and never the setup key, VNC password or SSH keys.
func TestTaskLogValueRedactsSecrets(t *testing.T) {
	tk := Task{TaskID: "t1", SandboxID: "s1", Kind: KindCreate, SetupKey: "SECRETKEY",
		VNCPassword: "SECRETPW", SSHPublicKeys: []string{"ssh-ed25519 SECRETSSH"}}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("task", "t", tk)
	out := buf.String()
	if strings.Contains(out, "SECRET") {
		t.Fatalf("slog output leaks a secret: %s", out)
	}
	for _, want := range []string{"t.task=t1", "t.sandbox=s1", "t.kind=" + string(KindCreate)} {
		if !strings.Contains(out, want) {
			t.Errorf("slog output missing %q: %s", want, out)
		}
	}
}
