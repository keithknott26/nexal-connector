package mesh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestProbeTTLKeepsLoopbackFresh(t *testing.T) {
	if got := probeTTL("127.0.0.1"); got != time.Minute {
		t.Errorf("probeTTL(loopback) = %v, want %v", got, time.Minute)
	}
	if got := probeTTL("100.64.0.7"); got != serviceProbeTTL {
		t.Errorf("probeTTL(peer) = %v, want %v", got, serviceProbeTTL)
	}
	if time.Minute >= serviceProbeTTL {
		t.Error("loopback TTL must be shorter than the peer TTL")
	}
}

func TestTrustedExecutableChecksSiblingPermissions(t *testing.T) {
	for _, fallback := range []string{"/usr/local/bin/netbird", "/opt/homebrew/bin/netbird"} {
		if _, err := os.Stat(fallback); err == nil {
			t.Skipf("%s exists; fallback would mask the sibling checks", fallback)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot locate test executable")
	}
	sibling := filepath.Join(filepath.Dir(self), "nexal-network")
	if _, err := os.Lstat(sibling); err == nil {
		t.Skip("sibling already exists")
	}
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Skipf("cannot create sibling: %v", err)
	}
	t.Cleanup(func() { os.Remove(sibling) })

	cases := []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o600, false}, // not executable
		{0o777, false}, // writable by group and others
		{0o775, false}, // group-writable
		{0o757, false}, // other-writable
		{0o755, true},
		{0o700, true},
	}
	for _, c := range cases {
		if err := os.Chmod(sibling, c.mode); err != nil {
			t.Fatal(err)
		}
		path, err := trustedExecutable("nexal-network")
		if c.ok && (err != nil || path != sibling) {
			t.Errorf("mode %o: got (%q, %v), want %q", c.mode, path, err, sibling)
		}
		if !c.ok && (err == nil || path != "") {
			t.Errorf("mode %o: got (%q, %v), want rejection", c.mode, path, err)
		}
	}
}
