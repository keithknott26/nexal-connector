package sharing

import (
	"context"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// The other tests replace the package seams; these drive the real ones.

func TestRealListeningProbeSeesALoopbackListenerAndItsAbsence(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	if !listening(context.Background(), p) {
		t.Fatalf("listening(%s) = false with a live listener", p)
	}
	_ = ln.Close()
	if listening(context.Background(), p) {
		t.Fatalf("listening(%s) = true after the listener closed", p)
	}
}

func TestRealRunRootReportsSuccessTruncatedOutputAndBareErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	ctx := context.Background()
	if err := runRoot(ctx, sh, "-c", "exit 0"); err != nil {
		t.Fatalf("successful command returned %v", err)
	}
	err = runRoot(ctx, sh, "-c", "echo boom; exit 3")
	if err == nil || err.Error() != "boom" {
		t.Fatalf("failed command error = %v, want the trimmed output", err)
	}
	err = runRoot(ctx, sh, "-c", "i=0; while [ $i -lt 400 ]; do printf x; i=$((i+1)); done; exit 1")
	if err == nil || !strings.HasSuffix(err.Error(), " ...(truncated)") || len(err.Error()) != maxCommandOutput+len(" ...(truncated)") {
		t.Fatalf("long output not truncated as expected: %v", err)
	}
	err = runRoot(ctx, sh, "-c", "exit 4")
	if err == nil || !strings.Contains(err.Error(), "exit status 4") {
		t.Fatalf("silent failure should surface the exec error, got %v", err)
	}
}

func TestRealRunRootDoesNotInheritTheEnvironment(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/evil.dylib")
	err = runRoot(context.Background(), sh, "-c", `[ -z "$DYLD_INSERT_LIBRARIES" ] || { echo leaked; exit 1; }`)
	if err != nil {
		t.Fatalf("environment leaked into the privileged command: %v", err)
	}
}
