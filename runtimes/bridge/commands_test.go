package runtimebridge

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func installation() LocalInstallation {
	return LocalInstallation{"/opt/nexal/venv/bin/python3", "/opt/nexal/nexal_mlx_entry.py", "/opt/nexal/config.json"}
}

func peers() []Peer {
	return []Peer{
		{strings.Repeat("a", 64), "192.168.50.10", 5100, "M4", true, installation(), "/opt/nexal/hosts.json"},
		{strings.Repeat("b", 64), "192.168.50.11", 5100, "M2", true, installation(), "/opt/nexal/hosts.json"},
	}
}

func TestInferenceArgsAreFixed(t *testing.T) {
	command, err := InferenceCommand(installation(), "/tmp/admission.json", "/tmp/hello;$(not-a-command).txt", 128)
	if err != nil {
		t.Fatal(err)
	}
	if command.Executable != installation().Python || command.Args[0] != "-I" ||
		command.Args[1] != "-B" || command.Args[9] != "/tmp/hello;$(not-a-command).txt" {
		t.Fatalf("unexpected argument array: %#v", command)
	}
	if _, err := InferenceCommand(installation(), "/tmp/a", "/tmp/p", 513); err == nil {
		t.Fatal("accepted unbounded generation")
	}
}

func TestRingPlanAndDigest(t *testing.T) {
	plan, err := PlanRingSmoke("ring", peers(), true)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plan.Hostfile)
	if plan.HostfileSHA256 != hex.EncodeToString(sum[:]) || len(plan.Ranks) != 2 ||
		plan.DistributedInferenceEnabled || plan.Ranks[1].Rank != 1 {
		t.Fatalf("invalid plan: %#v", plan)
	}
}

func TestDistributedGates(t *testing.T) {
	if _, err := PlanRingSmoke("ring", peers(), false); err == nil {
		t.Fatal("missing consent accepted")
	}
	if _, err := PlanRingSmoke("jaccl", peers(), true); err == nil {
		t.Fatal("base M4 JACCL accepted")
	}
	if _, err := PlanRingSmoke("any", peers(), true); err == nil {
		t.Fatal("backend auto-selection accepted")
	}
	for _, address := range []string{"0.0.0.0", "8.8.8.8", "127.0.0.1", "169.254.1.2", "host.local", "::1"} {
		p := peers()
		p[0].Address = address
		if _, err := PlanRingSmoke("ring", p, true); err == nil {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
	p := peers()
	p[1].Trusted = false
	if _, err := PlanRingSmoke("ring", p, true); err == nil {
		t.Fatal("untrusted peer accepted")
	}
	p = peers()
	p[1].Address = p[0].Address
	if _, err := PlanRingSmoke("ring", p, true); err == nil {
		t.Fatal("duplicate address accepted")
	}
}

func TestLocalPaths(t *testing.T) {
	for _, path := range []string{"python3", "/a/../b", "https://example.test/python", "/tmp/a\nb"} {
		i := installation()
		i.Python = path
		if _, err := InferenceCommand(i, "/tmp/a", "/tmp/b", 1); err == nil {
			t.Fatalf("bad path accepted: %q", path)
		}
	}
}
