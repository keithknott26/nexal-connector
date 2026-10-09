package inference

import "fmt"

// Preflight heuristics for link quality. They are NOT measured requirements of
// the MLX collective (none exist: multi-host execution is not shipped); they only
// decide when the report should warn that a link looks unsuitable for sharing.
const (
	// flapUnstableThreshold: direct<->relay path changes in the last hour at or
	// above which a link is called unstable (mesh.Peer.PathFlapsLastHour). It is
	// the same threshold the network panel uses for "Connection unstable"
	// (NetworkPanel.swift unstableFlapThreshold), so the two never disagree.
	flapUnstableThreshold = 6
	// slowLinkMbps: measured download throughput below this is "fair" at best.
	slowLinkMbps = 100.0
	// highLatencyMS: round-trip above this is "fair" at best.
	highLatencyMS = 25.0
	// lossPoorPercent: packet loss at or above this is "poor".
	lossPoorPercent = 2.0
)

func linkIsUp(l Link) bool {
	return l.Lifecycle == "connected" || l.Lifecycle == "degraded"
}

func ptr(v float64) *float64 { return &v }

// buildLinkReport classifies one link. online is the peer's online flag.
func buildLinkReport(h Host, l *Link) LinkReport {
	r := LinkReport{PeerID: h.ID, PeerName: h.Name, Online: h.Online, Path: "unknown", PQ: "unknown",
		Transport: "tcp-over-mesh", RDMA: "unknown", Notes: []string{}}
	r.Notes = append(r.Notes, "RDMA and Thunderbolt are not reported by any layer, so they stay unknown; sharing would run over TCP.")
	if l == nil {
		r.Quality = "unusable"
		if h.Online {
			r.Quality = "poor"
		}
		r.Notes = append(r.Notes, "No mesh link information for this Mac.")
		return r
	}
	r.Path, r.PathLabel, r.DirectVia = l.Path, l.PathLabel, l.DirectVia
	if r.Path == "" {
		r.Path = "unknown"
	}
	r.PQ, r.PQReason, r.PathFlapsLastHour = l.PQ, l.PQReason, l.PathFlapsLastHour
	if r.PQ == "" {
		r.PQ = "unknown"
	}
	if l.LatencyKnown {
		r.LatencyMS = ptr(l.LatencyMS)
	}
	if l.BandwidthKnown {
		r.BandwidthMbps = ptr(l.BandwidthMbps)
	}
	if l.LossKnown {
		r.PacketLossPercent = ptr(l.PacketLossPercent)
	}
	r.Unstable = l.PathFlapsLastHour >= flapUnstableThreshold

	if !h.Online || !linkIsUp(*l) {
		r.Quality = "unusable"
		r.Notes = append(r.Notes, "The link is not up right now.")
		return r
	}
	poor, fair := false, false
	if r.Path != "direct" {
		poor = true
		r.Notes = append(r.Notes, fmt.Sprintf("Traffic goes by %s path, not directly between the two Macs.", r.Path))
	}
	if r.Unstable {
		poor = true
		r.Notes = append(r.Notes, fmt.Sprintf("The path switched between direct and relay %d times in the last hour.", l.PathFlapsLastHour))
	}
	if l.LossKnown && l.PacketLossPercent >= lossPoorPercent {
		poor = true
		r.Notes = append(r.Notes, fmt.Sprintf("Packet loss is %.1f%%.", l.PacketLossPercent))
	}
	if !l.BandwidthKnown {
		fair = true
		r.Notes = append(r.Notes, "Bandwidth has not been measured yet (the connector measures it periodically).")
	} else if l.BandwidthMbps < slowLinkMbps {
		fair = true
		r.Notes = append(r.Notes, fmt.Sprintf("Measured bandwidth is %.0f Mbps.", l.BandwidthMbps))
	}
	if l.LatencyKnown && l.LatencyMS > highLatencyMS {
		fair = true
		r.Notes = append(r.Notes, fmt.Sprintf("Round-trip latency is %.0f ms.", l.LatencyMS))
	}
	if !l.LatencyKnown {
		fair = true
		r.Notes = append(r.Notes, "Latency has not been measured.")
	}
	switch {
	case poor:
		r.Quality = "poor"
	case fair:
		r.Quality = "fair"
	default:
		r.Quality = "good"
	}
	return r
}
