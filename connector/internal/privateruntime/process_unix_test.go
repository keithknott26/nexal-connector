//go:build darwin || linux

package privateruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessHasNoInheritedCredentialAndCancels(t *testing.T) {
	root := t.TempDir()
	lease := filepath.Join(root, "session.json")
	if err := os.WriteFile(lease, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXAL_INFERENCE_TOKEN", "must-not-reach-private-child")
	script := []byte("#!/bin/sh\n[ -z \"${NEXAL_INFERENCE_TOKEN:-}\" ] || exit 99\n[ \"$1\" = '--session-file' ] || exit 98\n[ -f \"$2\" ] || exit 97\nprintf ready > ready\n/bin/sleep 30 &\nwait\n")
	if err := os.WriteFile(filepath.Join(root, "runtime"), script, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runProcess(ctx, root, lease) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime did not start with isolated environment")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled process succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime ignored cancellation")
	}
}
