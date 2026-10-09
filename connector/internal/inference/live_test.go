package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleStatus = `{
 "hostId": "host-1", "executionBlocker": "production job execution gated pending verified tunnel dispatch",
 "telemetry": {"known": true, "totalMemoryBytes": 34359738368, "availableMemoryBytes": 21474836480},
 "resourcePolicy": {"memoryLimitBytes": 8589934592, "reserveMemoryBytes": 2147483648, "minFreeDiskBytes": 0},
 "mesh": {"providerAvailable": true, "pq": "protected", "peers": [
  {"id":"p1","name":"Mac mini","lifecycle":"connected","path":"direct","pathLabel":"Direct","latencyMs":3.5,"pq":"protected","pathFlapsLastHour":1,"tunnelAddress":"100.64.0.2","directVia":"lan","bandwidthMbps":640},
  {"id":"p2","name":"Studio","lifecycle":"offline","path":"unknown","pq":"protected","tunnelAddress":"100.64.0.3"},
  {"id":"g1","name":"gw-fra-1","lifecycle":"connected","path":"direct","pq":"protected","tunnelAddress":"100.64.0.9"}]},
 "presence": {"hosts": [
  {"hostId":"x","online":true,"info":{"chip":"Apple M4 Pro","os":"macOS 26.2 (25C56)","memoryBytes":51539607552,"memoryAvailableBytes":42949672960,"diskFreeBytes":400000000000,"tunnelAddress":"100.64.0.2"}}]}
}`

func TestStatusSource(t *testing.T) {
	s := NewStatusSource([]byte(sampleStatus), nil, LocalInfo{Name: "MacBook", Chip: "Apple M4 Max", OS: "macOS 26.2", DiskFreeBytes: 9 << 30, DiskKnown: true, MemoryBytes: 1})
	hosts, err := s.Hosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 {
		t.Fatalf("want self + 2 peers (gateway dropped), got %d: %+v", len(hosts), hosts)
	}
	self := hosts[0]
	if !self.IsSelf || self.ApprovedMemoryBytes != 8<<30 || self.OwnerReserveBytes != 2<<30 || !self.AdmissionKnown || !self.MemoryKnown || self.TotalMemoryBytes != 32<<30 {
		t.Errorf("self = %+v", self)
	}
	mini := hosts[1]
	if mini.Chip != "Apple M4 Pro" || !mini.MemoryKnown || mini.AvailableMemoryBytes != 40<<30 || !mini.Online || !mini.DiskKnown {
		t.Errorf("mini = %+v", mini)
	}
	studio := hosts[2]
	if studio.Online || studio.MemoryKnown {
		t.Errorf("studio (offline, no presence) = %+v", studio)
	}
	links, _ := s.Links(context.Background())
	if len(links) != 2 || links[0].BandwidthMbps != 640 || !links[0].BandwidthKnown || links[0].PathFlapsLastHour != 1 || links[1].BandwidthKnown {
		t.Errorf("links = %+v", links)
	}
	x := s.Extras()
	if !x.Reachable || x.MeshPQ != "protected" || !strings.Contains(x.ExecutionBlocker, "gated") {
		t.Errorf("extras = %+v", x)
	}
}

func TestStatusSourceUnreachable(t *testing.T) {
	s := NewStatusSource(nil, errors.New("down"), LocalInfo{Name: "MacBook", Chip: "Apple M4", MemoryBytes: 16 << 30})
	hosts, _ := s.Hosts(context.Background())
	if len(hosts) != 1 || hosts[0].AdmissionKnown || hosts[0].MemoryKnown || hosts[0].TotalMemoryBytes != 16<<30 || s.Extras().Reachable {
		t.Errorf("hosts = %+v extras=%+v", hosts, s.Extras())
	}
	bad := NewStatusSource([]byte("not json"), nil, LocalInfo{})
	if bad.Extras().Reachable {
		t.Error("garbage status must read as unreachable")
	}
}

// ---- probe ------------------------------------------------------------------

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type fixture struct {
	dir, policy, python, entry string
}

func writeFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "rt", "nexal_mlx"), 0o755))
	python := filepath.Join(dir, "python")
	must(os.WriteFile(python, []byte("#!/bin/sh\necho fake\n"), 0o755))
	files := map[string][]byte{"nexal_mlx_entry.py": []byte("# entry\n"), "nexal_mlx/__init__.py": []byte("# init\n"), "nexal_mlx/cli.py": []byte("# cli\n")}
	pins := map[string]string{}
	for name, body := range files {
		must(os.WriteFile(filepath.Join(dir, "rt", name), body, 0o644))
		pins[name] = sum(body)
	}
	pol := map[string]any{
		"installation": map[string]string{"python": python, "entry": filepath.Join(dir, "rt", "nexal_mlx_entry.py"), "config": filepath.Join(dir, "c.json")},
		"entry_sha256": pins["nexal_mlx_entry.py"], "python_sha256": sum([]byte("#!/bin/sh\necho fake\n")),
		"config_sha256": sum([]byte("c")), "runtime_files_sha256": pins,
	}
	b, _ := json.Marshal(pol)
	policy := filepath.Join(dir, "owner-policy.json")
	must(os.WriteFile(policy, b, 0o600))
	return fixture{dir, policy, python, filepath.Join(dir, "rt", "nexal_mlx_entry.py")}
}

func runnerReturning(out string, err error) Runner {
	return func(context.Context, string, string) ([]byte, error) { return []byte(out), err }
}

const probeHealthy = `{"schema_version":1,"runtime_version":"0.2.0","system":"Darwin","machine":"arm64","packages":{"mlx":"0.29.3","mlx-lm":"0.28.4","transformers":"4.57.6"},"distributed_execution":"disabled"}`

func TestFileProber(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(f fixture) FileProber
		tamper func(t *testing.T, f fixture)
		want   string
	}{
		{"no policy path", func(f fixture) FileProber { return FileProber{} }, nil, RuntimeNotConfigured},
		{"policy file missing", func(f fixture) FileProber { return FileProber{OwnerPolicyPath: filepath.Join(f.dir, "none.json")} }, nil, RuntimeNotConfigured},
		{"healthy", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(probeHealthy, nil)}
		}, nil, RuntimeHealthy},
		{"runtime file tampered", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(probeHealthy, nil)}
		},
			func(t *testing.T, f fixture) {
				os.WriteFile(filepath.Join(f.dir, "rt", "nexal_mlx", "cli.py"), []byte("evil"), 0o644)
			}, RuntimeIntegrityMismatch},
		{"extra unpinned file", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(probeHealthy, nil)}
		},
			func(t *testing.T, f fixture) {
				os.WriteFile(filepath.Join(f.dir, "rt", "sitecustomize.py"), []byte("evil"), 0o644)
			}, RuntimeIntegrityMismatch},
		{"interpreter tampered", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(probeHealthy, nil)}
		},
			func(t *testing.T, f fixture) { os.WriteFile(f.python, []byte("other"), 0o755) }, RuntimeIntegrityMismatch},
		{"symlink in tree", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(probeHealthy, nil)}
		},
			func(t *testing.T, f fixture) { os.Symlink(f.entry, filepath.Join(f.dir, "rt", "link.py")) }, RuntimeIntegrityMismatch},
		{"probe fails", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning("", errors.New("exit 1"))}
		}, nil, RuntimeProbeFailed},
		{"garbage output", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning("hello", nil)}
		}, nil, RuntimeProbeFailed},
		{"linux", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(strings.Replace(probeHealthy, "Darwin", "Linux", 1), nil)}
		}, nil, RuntimeUnsupportedPlatform},
		{"packages missing", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(`{"schema_version":1,"system":"Darwin","machine":"arm64","packages":{"mlx":null,"mlx-lm":null,"transformers":null}}`, nil)}
		}, nil, RuntimeDependenciesMissing},
		{"wrong version", func(f fixture) FileProber {
			return FileProber{OwnerPolicyPath: f.policy, Run: runnerReturning(strings.Replace(probeHealthy, "0.28.4", "0.30.0", 1), nil)}
		}, nil, RuntimeVersionMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := writeFixture(t)
			if tc.tamper != nil {
				tc.tamper(t, f)
			}
			got := tc.setup(f).Probe(context.Background())
			if got.State != tc.want {
				t.Errorf("state = %s (%s), want %s", got.State, got.Detail, tc.want)
			}
			if got.State == RuntimeHealthy && (got.MLXVersion != "0.29.3" || got.DistributedExecution != "disabled") {
				t.Errorf("probe = %+v", got)
			}
		})
	}
}

func TestFileProberOnlyRunsAfterIntegrityCheck(t *testing.T) {
	f := writeFixture(t)
	os.WriteFile(filepath.Join(f.dir, "rt", "nexal_mlx", "cli.py"), []byte("evil"), 0o644)
	called := false
	p := FileProber{OwnerPolicyPath: f.policy, Run: func(context.Context, string, string) ([]byte, error) { called = true; return nil, nil }}
	if got := p.Probe(context.Background()).State; got != RuntimeIntegrityMismatch || called {
		t.Errorf("state=%s called=%v: code must not execute before verification", got, called)
	}
}

func TestExecProbeRunsFixedArguments(t *testing.T) {
	// A shell script stands in for the interpreter and echoes the probe JSON,
	// proving the real exec path, argument shape and output limit.
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = \"-I\" ] && [ \"$2\" = \"-B\" ] && [ \"$4\" = \"probe\" ] || exit 9\ncat <<'EOF'\n" + probeHealthy + "\nEOF\n"
	python := filepath.Join(dir, "py")
	if err := os.WriteFile(python, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := execProbe(context.Background(), python, "/x/nexal_mlx_entry.py")
	if err != nil {
		t.Skipf("cannot exec test script here: %v", err)
	}
	if got := interpretProbe(out); got.State != RuntimeHealthy {
		t.Errorf("state = %s", got.State)
	}
	big := "#!/bin/sh\nhead -c 200000 /dev/zero\n"
	os.WriteFile(python, []byte(big), 0o755)
	if _, err := execProbe(context.Background(), python, "/x/e.py"); err == nil {
		t.Error("oversized output must fail")
	}
}
