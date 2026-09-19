package lab

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"nexal/experiments/tcp-pager/pager"
)

func prepared(t *testing.T) (string, Manifest) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	m, err := Prepare(state, "127.0.0.1:9443", true)
	if err != nil {
		t.Fatal(err)
	}
	return state, m
}

func rewrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAndPrivateImport(t *testing.T) {
	state, want := prepared(t)
	client := filepath.Join(state, "client")
	// Transfer tools may relax read permissions. Import must not preserve them.
	if err := os.Chmod(filepath.Join(client, "key.pem"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client, "do-not-run.sh"), []byte("exit 1"), 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "imported")
	got, err := Import(client, dest, true)
	if err != nil || got != want {
		t.Fatalf("%v %+v", err, got)
	}
	for _, p := range []string{dest, filepath.Join(dest, "key.pem")} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm()&0077 != 0 {
			t.Fatalf("private mode: %s %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "do-not-run.sh")); !os.IsNotExist(err) {
		t.Fatal("copied extra file")
	}
	if _, err := Import(client, dest, true); err == nil {
		t.Fatal("overwrote destination")
	}
	if _, err := Prepare(state, want.Endpoint, true); err == nil {
		t.Fatal("overwrote donor")
	}
}

func TestEndpointPolicy(t *testing.T) {
	for _, addr := range []string{"8.8.8.8:9443", "0.0.0.0:9443", "example.com:9443",
		"127.0.0.1:9443", "192.168.1.2:0", "192.168.1.2:65536", "192.168.1.2:https"} {
		t.Run(addr, func(t *testing.T) {
			if ValidateEndpoint(addr, false) == nil {
				t.Fatal("accepted bad endpoint")
			}
		})
	}
	for _, addr := range []string{"192.168.1.2:9443", "[fd00::1]:9443"} {
		if err := ValidateEndpoint(addr, false); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := IsLocal("127.0.0.1:9443"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := Prepare(filepath.Join(t.TempDir(), "new"), "203.0.113.1:9443", false); err == nil {
		t.Fatal("accepted nonlocal public donor")
	}
}

func TestManifestRejectsAlterations(t *testing.T) {
	for _, mode := range []string{"expired", "fingerprint", "unknown", "trailing", "capacity", "symlink", "oversize", "shared-writable"} {
		t.Run(mode, func(t *testing.T) {
			state, m := prepared(t)
			dir := filepath.Join(state, "client")
			file := filepath.Join(dir, "connection.json")
			switch mode {
			case "expired":
				m.Expires = time.Now().Add(-time.Hour)
			case "fingerprint":
				m.CAFingerprint = strings.Repeat("0", 64)
			case "capacity":
				m.Pages = 256
			}
			b, _ := json.Marshal(m)
			switch mode {
			case "unknown":
				b = append([]byte(`{"unknown":true,`), b[1:]...)
			case "trailing":
				b = append(b, []byte(` {}`)...)
			case "oversize":
				b = []byte(strings.Repeat(" ", MaxFile+1))
			}
			rewrite(t, file, b)
			if mode == "symlink" {
				if err := os.Rename(file, file+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".real", file); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "shared-writable" {
				if err := os.Chmod(file, 0666); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadManifest(dir, true); err == nil {
				t.Fatal("accepted altered manifest")
			}
		})
	}
}

func TestImportRejectsKeySymlink(t *testing.T) {
	state, _ := prepared(t)
	dir := filepath.Join(state, "client")
	key := filepath.Join(dir, "key.pem")
	if err := os.Rename(key, key+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(key+".real", key); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(dir, filepath.Join(t.TempDir(), "out"), true); err == nil {
		t.Fatal("accepted symlink")
	}
}

func startLab(t *testing.T, state string, sessions int) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conf, err := pager.LoadTLS(filepath.Join(state, "donor"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- pager.ServeLab(ctx, ln, conf, Pages, sessions) }()
	for _, role := range []string{"client", "donor"} {
		dir := filepath.Join(state, role)
		m, err := LoadManifest(dir, true)
		if err != nil {
			t.Fatal(err)
		}
		m.Endpoint = ln.Addr().String()
		b, _ := json.Marshal(m)
		rewrite(t, filepath.Join(dir, "connection.json"), b)
	}
	return ln.Addr().String(), done
}

func TestRepeatedPortableReceiversUseFreshStores(t *testing.T) {
	state, _ := prepared(t)
	_, done := startLab(t, state, 2)
	for i := 0; i < 2; i++ {
		output := filepath.Join(t.TempDir(), "receiver")
		r, err := Receive(context.Background(), filepath.Join(state, "client"), output, "", true, true)
		if err != nil {
			t.Fatal(err)
		}
		if !r.PagingSuitePassed || r.NativeSuiteExecuted || r.NonLocalEndpoint ||
			r.HostRAMExpansionPassed || r.GuestOSRAMExpansionPassed || r.GPUMemoryExpansionPassed ||
			r.SeparatePhysicalMachinesAttested || r.OSMemoryRequirement != "NOT_IMPLEMENTED" {
			t.Fatalf("false acceptance claim: %+v", r)
		}
		b, err := os.ReadFile(filepath.Join(output, "report.json"))
		if err != nil || !json.Valid(b) {
			t.Fatal("report not saved", err)
		}
		if strings.Contains(string(b), "PRIVATE KEY") {
			t.Fatal("credential leak")
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lab did not stop at session cap")
	}
}

func TestReceiverFailureSavesFailingEvidence(t *testing.T) {
	state, _ := prepared(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // An actual refused connection, no synthesized successful report.
	m, _ := LoadManifest(filepath.Join(state, "client"), true)
	m.Endpoint = addr
	b, _ := json.Marshal(m)
	rewrite(t, filepath.Join(state, "client", "connection.json"), b)
	output := filepath.Join(t.TempDir(), "failed")
	r, err := Receive(context.Background(), filepath.Join(state, "client"), output, "", true, true)
	if err == nil || r.PagingSuitePassed || len(r.Cases) != 1 || r.Cases[0].Error == "" {
		t.Fatal("missing connection failure")
	}
	if _, err := os.Stat(filepath.Join(output, "report.json")); err != nil {
		t.Fatal(err)
	}
}

func TestLoopbackRequiresExplicitTestOptIn(t *testing.T) {
	state, _ := prepared(t)
	if _, err := LoadManifest(filepath.Join(state, "client"), false); err == nil {
		t.Fatal("loopback allowed")
	}
}

func TestNativeCannotRunOnLinux(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Linux gate only")
	}
	_, err := Receive(context.Background(), "unused", filepath.Join(t.TempDir(), "out"), "/bin/true", false, true)
	if err == nil {
		t.Fatal("native accepted on Linux")
	}
}

func TestResultChecksRejectClaimsAndBadCounters(t *testing.T) {
	state, _ := prepared(t)
	_, done := startLab(t, state, 1)
	r, err := Receive(context.Background(), filepath.Join(state, "client"), filepath.Join(t.TempDir(), "out"), "", true, true)
	if err != nil {
		t.Fatal(err)
	}
	base := r.Cases[0].Paging
	for _, mutate := range []func(*pager.Report){
		func(r *pager.Report) { r.HostRAMExpanded = true },
		func(r *pager.Report) { r.MacOSGuestBooted = true },
		func(r *pager.Report) { r.GPUMemoryExpanded = true },
		func(r *pager.Report) { r.NativeHVFExecuted = true },
		func(r *pager.Report) { r.Transport.PageBytesReceived-- },
		func(r *pager.Report) { r.Cache.PeakResidentPages++ },
		func(r *pager.Report) { r.Verified = false },
	} {
		got := base
		mutate(&got)
		if CheckResult(got, 4, false) == nil {
			t.Fatal("bad result accepted")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCannotInventLinuxMemory(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Linux observation only")
	}
	s := Snapshot(context.Background())
	if s.Available || s.PhysicalBytes != 0 {
		t.Fatal("invented Mac memory")
	}
}

func TestMemoryCounterClassificationIsNotExpansionProof(t *testing.T) {
	before := MemorySnapshot{Available: true, PhysicalBytes: 8589934592}
	r := Report{Before: before, After: before}
	status, n := classifyMemory(r)
	if status != "unchanged_in_available_samples" || n != 2 {
		t.Fatal(status, n)
	}
	r.Cases = []CaseResult{{HostMemoryDuring: []MemorySnapshot{
		{Available: false}, {Available: true, PhysicalBytes: 8589934593},
	}}}
	status, n = classifyMemory(r)
	if status != "counter_changed_requires_investigation_not_proof_of_expansion" || n != 3 {
		t.Fatal(status, n)
	}
	if r.HostRAMExpansionPassed {
		t.Fatal("counter change promoted to proof")
	}
	status, n = classifyMemory(Report{})
	if status != "insufficient_available_measurements" || n != 0 {
		t.Fatal(status, n)
	}
}

func TestMemoryObserverStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := observeMemory(ctx)
	cancel()
	done := make(chan struct{})
	go func() { _ = stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer leaked after cancellation")
	}
}
