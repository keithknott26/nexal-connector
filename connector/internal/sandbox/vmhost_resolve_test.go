package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveVMHost(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none")
	good := filepath.Join(dir, "nexal-vmhost")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExec := filepath.Join(dir, "plain")
	_ = os.WriteFile(notExec, []byte("x"), 0o644)
	if p, all := ResolveVMHost("", []string{missing, notExec, good}); p != good || len(all) != 3 {
		t.Fatalf("got %q %v", p, all)
	}
	if p, _ := ResolveVMHost(good, []string{missing}); p != good {
		t.Fatalf("preferred: %q", p)
	}
	if p, all := ResolveVMHost(missing, []string{notExec}); p != "" || len(all) != 2 {
		t.Fatalf("none: %q %v", p, all)
	}
}

func TestVMHostCandidatesIncludeBundleAndSupportDir(t *testing.T) {
	c := VMHostCandidates("/base")
	want := map[string]bool{"/base/bin/nexal-vmhost": false, "/Applications/neXal-Connector.app/Contents/Helpers/nexal-vmhost": false}
	for _, p := range c {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Errorf("missing candidate %s", p)
		}
	}
}
