package sandbox

import (
	"strings"
	"testing"
)

func TestAckIf(t *testing.T) {
	if got := ackIf(true, "t1"); got != "t1" {
		t.Fatalf("ackIf(true) = %q, want t1", got)
	}
	if got := ackIf(false, "t1"); got != "" {
		t.Fatalf("ackIf(false) = %q, want empty", got)
	}
}

func TestOfferable(t *testing.T) {
	f := HostFacts{CPUs: 10, MemoryMB: 16384}
	c := Caps{MaxCPUFraction: 0.5, MaxMemFraction: 0.5}

	// Overcommitted CPUs (default): one instance may use every core.
	cpus, mem := offerable(c, f)
	if cpus != 10 || mem != 8192 {
		t.Fatalf("overcommit offerable = %d cpus, %d MB; want 10, 8192", cpus, mem)
	}

	// StrictCPU applies the fraction to cores too.
	c.StrictCPU = true
	cpus, mem = offerable(c, f)
	if cpus != 5 || mem != 8192 {
		t.Fatalf("strict offerable = %d cpus, %d MB; want 5, 8192", cpus, mem)
	}
}

func TestDevUpSpecStringOmitsSecrets(t *testing.T) {
	s := DevUpSpec{Workspace: "nexal-sb1", DriveToken: "super-secret"}
	got := s.String()
	if !strings.Contains(got, "nexal-sb1") {
		t.Fatalf("String() = %q, want the workspace name", got)
	}
	if strings.Contains(got, "super-secret") {
		t.Fatalf("String() leaked the drive token: %q", got)
	}
}

func TestDefaultHostingConfigPath(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	p, err := DefaultHostingConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/Users/test/Library/Application Support/Nexal/sandbox-hosting.json"; p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
}
