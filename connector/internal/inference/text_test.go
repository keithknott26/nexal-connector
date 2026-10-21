package inference

import (
	"strings"
	"testing"
)

func TestGibU(t *testing.T) {
	for _, c := range []struct {
		in   uint64
		want string
	}{{0, "0.0 GiB"}, {1 << 30, "1.0 GiB"}, {3 << 29, "1.5 GiB"}, {24 << 30, "24.0 GiB"}} {
		if got := gibU(c.in); got != c.want {
			t.Errorf("gibU(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOrDash(t *testing.T) {
	if orDash("") != "-" || orDash("M3") != "M3" {
		t.Error("orDash should map only the empty string to a dash")
	}
}

func TestRenderTextMinimalReportOmitsOptionalSections(t *testing.T) {
	txt := RenderText(Report{Headline: "nothing fits"})
	for _, absent := range []string{"Links from this Mac", "Things in the way", "What this check could not see"} {
		if strings.Contains(txt, absent) {
			t.Errorf("empty report should not contain section %q", absent)
		}
	}
	if !strings.Contains(txt, "Recommended: -") {
		t.Errorf("missing recommendation should render a dash:\n%s", txt)
	}
}

func TestRenderTextIneligibleMachineAndUnmeasuredLink(t *testing.T) {
	bw := 450.4
	txt := RenderText(Report{
		Machines: []MachineReport{{Name: "Mini", Eligible: false, Reasons: []string{"offline.", "no runtime."}, Runtime: RuntimeReport{State: "not_installed"}}},
		Links: []LinkReport{
			{PeerName: "Mini", Quality: "poor", Path: "relay", PQ: "yes", PathFlapsLastHour: 3, BandwidthMbps: &bw},
		},
		Blockers: []Blocker{{Severity: SeverityBlocker, Title: "t", Detail: "d", Fix: "f", FixCommand: "nexal x"}},
	})
	for _, want := range []string{
		"Mini: -, -, 0.0 GiB total",
		"MLX runtime: not installed. not used: offline. no runtime.",
		"450 Mbps, latency not measured, post-quantum yes, 3 path flaps",
		"Command: nexal x",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
}
