package inference

import (
	"strings"
	"testing"
)

func TestParseMacOS(t *testing.T) {
	cases := []struct {
		in           string
		major, minor int
	}{
		{"macOS 15.4", 15, 4},
		{"Darwin macOS 14.0.1", 14, 0},
		{"Linux 6.1", 0, 0},
		{"", 0, 0},
		{"macOS garbage", 0, 0},
	}
	for _, c := range cases {
		maj, min := parseMacOS(c.in)
		if maj != c.major || min != c.minor {
			t.Errorf("parseMacOS(%q) = %d.%d, want %d.%d", c.in, maj, min, c.major, c.minor)
		}
	}
}

func TestGib(t *testing.T) {
	if got := gib(3 << 29); got != "1.5 GiB" {
		t.Errorf("gib = %q", got)
	}
	if got := gib(0); got != "0.0 GiB" {
		t.Errorf("gib(0) = %q", got)
	}
}

func TestDeviceIDAndPlanningDigestAreDistinct64Hex(t *testing.T) {
	d, p := deviceID("x"), planningDigest("x")
	if len(d) != 64 || len(p) != 64 {
		t.Fatalf("lengths %d %d", len(d), len(p))
	}
	if d == p {
		t.Error("deviceID and planningDigest must be domain-separated")
	}
	if deviceID("x") != d || deviceID("y") == d {
		t.Error("deviceID must be deterministic and input-dependent")
	}
}

func TestDiskCheck(t *testing.T) {
	n := &node{host: Host{Name: "mini"}}
	if ok, msg := diskCheck(n, 1<<40); !ok || msg != "" {
		t.Errorf("unknown disk must pass, got %v %q", ok, msg)
	}
	n.host.DiskKnown = true
	n.host.DiskFreeBytes = 100 << 30
	if ok, _ := diskCheck(n, 50<<30); !ok {
		t.Error("100G free, 10G default floor, 50G need should pass")
	}
	ok, msg := diskCheck(n, 95<<30)
	if ok || !strings.Contains(msg, "mini") || !strings.Contains(msg, "10.0 GiB") {
		t.Errorf("default floor not applied: %v %q", ok, msg)
	}
	n.host.DiskFreeBytes = 5 << 30
	if ok, _ := diskCheck(n, 0); ok {
		t.Error("free below floor must fail even with zero need")
	}
}

func TestHeadline(t *testing.T) {
	var r Report
	if got := headline(r); !strings.HasPrefix(got, "No catalog model") {
		t.Errorf("empty: %q", got)
	}
	r.Recommendation = Recommendation{ModelID: "m", DisplayName: "Model", HostName: "mini", RunnableNow: true}
	if got := headline(r); !strings.Contains(got, "can run now: Model on mini") {
		t.Errorf("runnable: %q", got)
	}
	r.Recommendation.RunnableNow = false
	if got := headline(r); !strings.Contains(got, "cannot run until") {
		t.Errorf("blocked: %q", got)
	}
}
