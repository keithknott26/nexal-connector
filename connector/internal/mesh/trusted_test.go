package mesh

import (
	"context"
	"errors"
	"testing"
)

func TestTrustedExecutableRejectsUnknownName(t *testing.T) {
	for _, name := range []string{"", "netbird", "../nexal-network", "sh"} {
		if path, err := trustedExecutable(name); err == nil || path != "" {
			t.Errorf("trustedExecutable(%q) = (%q, %v), want error", name, path, err)
		}
	}
}

func TestPeerAddressByNameBlankName(t *testing.T) {
	for _, name := range []string{"", "   ", "."} {
		if ip, ok := PeerAddressByName(context.Background(), name); ok || ip != "" {
			t.Errorf("PeerAddressByName(%q) = (%q, %v), want empty", name, ip, ok)
		}
	}
}

func TestStaticPlanStoreLoad(t *testing.T) {
	want := StartupPlan{}
	got, err := StaticPlanStore{Plan: want}.Load(context.Background())
	if err != nil || got != want {
		t.Errorf("Load() = (%+v, %v), want (%+v, nil)", got, err, want)
	}
}

func TestRunErrorMessageAndUnwrap(t *testing.T) {
	base := errors.New("exit status 1")
	err := &RunError{Err: base, Output: "daemon is not running"}
	if err.Error() != base.Error() {
		t.Errorf("Error() = %q, want %q (runtime output must stay out of the message)", err.Error(), base.Error())
	}
	if !errors.Is(err, base) {
		t.Error("errors.Is(RunError, base) = false, want true via Unwrap")
	}
}

func TestExecRunnerRejectsUnknownCommand(t *testing.T) {
	if err := (ExecRunner{}).Run(context.Background(), "sh", "-c", "true"); err == nil {
		t.Error("ExecRunner.Run(sh) succeeded, want unsupported runtime error")
	}
}
