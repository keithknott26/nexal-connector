package mesh

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRuntimeDetailScrubsCredentialPathAndTrims(t *testing.T) {
	err := &RunError{Err: errors.New("exit status 1"), Output: "reading /tmp/nexal-mesh-credential-123\nfailed to connect to daemon\n"}
	got := runtimeDetail(err, "/tmp/nexal-mesh-credential-123")
	if strings.Contains(got, "credential-123") || !strings.Contains(got, "failed to connect to daemon") || strings.Contains(got, "\n") {
		t.Fatalf("got %q", got)
	}
	long := &RunError{Err: errors.New("x"), Output: strings.Repeat("a", 1000)}
	if len(runtimeDetail(long, "")) > 310 {
		t.Fatal("not trimmed")
	}
	if runtimeDetail(errors.New("plain"), "") != "" {
		t.Fatal("plain error should have no detail")
	}
}

// With no trusted runtime installed, ReadRuntimeDetail must return an error
// and an empty detail rather than a partially populated one.
func TestReadRuntimeDetailErrorsWithoutRuntime(t *testing.T) {
	if _, err := trustedExecutable("nexal-network"); err == nil {
		t.Skip("a trusted runtime is installed; cannot exercise the missing-runtime path")
	}
	d, err := ReadRuntimeDetail(context.Background())
	if err == nil {
		t.Fatal("ReadRuntimeDetail without runtime returned nil error")
	}
	if d.DaemonVersion != "" || len(d.Peers) != 0 || len(d.Networks) != 0 {
		t.Errorf("detail = %+v; want zero value", d)
	}
}
