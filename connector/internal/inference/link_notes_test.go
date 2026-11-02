package inference

import (
	"strings"
	"testing"
)

// The report's Notes are what the user reads to learn why a link is rated as it is,
// so each rating reason must produce its own explanation and a good link only the
// standing RDMA disclaimer.
func TestLinkReportNotesExplainEachDowngrade(t *testing.T) {
	online := Host{ID: "p", Name: "P", Online: true}
	good := Link{Lifecycle: "connected", Path: "direct", LatencyKnown: true, LatencyMS: 3, BandwidthKnown: true, BandwidthMbps: 800}

	joined := func(l *Link, h Host) string { return strings.Join(buildLinkReport(h, l).Notes, "\n") }

	if n := buildLinkReport(online, &good).Notes; len(n) != 1 || !strings.Contains(n[0], "RDMA") {
		t.Errorf("good link notes = %q, want only the RDMA disclaimer", n)
	}

	relay := good
	relay.Path = "relay"
	if s := joined(&relay, online); !strings.Contains(s, "relay path, not directly") {
		t.Errorf("relay note missing: %q", s)
	}

	flappy := good
	flappy.PathFlapsLastHour = flapUnstableThreshold + 1
	if s := joined(&flappy, online); !strings.Contains(s, "7 times in the last hour") {
		t.Errorf("flap note missing count: %q", s)
	}

	lossy := good
	lossy.LossKnown, lossy.PacketLossPercent = true, 3.5
	if s := joined(&lossy, online); !strings.Contains(s, "Packet loss is 3.5%.") {
		t.Errorf("loss note missing: %q", s)
	}

	slow := good
	slow.BandwidthMbps, slow.LatencyMS = 50, 40
	s := joined(&slow, online)
	if !strings.Contains(s, "Measured bandwidth is 50 Mbps.") || !strings.Contains(s, "Round-trip latency is 40 ms.") {
		t.Errorf("slow link notes missing: %q", s)
	}

	unmeasured := Link{Lifecycle: "connected", Path: "direct"}
	s = joined(&unmeasured, online)
	if !strings.Contains(s, "Bandwidth has not been measured") || !strings.Contains(s, "Latency has not been measured") {
		t.Errorf("unmeasured notes missing: %q", s)
	}

	down := Link{Lifecycle: "authenticating", Path: "direct"}
	if s := joined(&down, online); !strings.Contains(s, "not up right now") {
		t.Errorf("down note missing: %q", s)
	}
	if s := joined(nil, online); !strings.Contains(s, "No mesh link information") {
		t.Errorf("nil link note missing: %q", s)
	}
}
