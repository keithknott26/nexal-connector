package inference

import (
	"fmt"
	"strings"
)

func gibU(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

// RenderText is the plain-language report for `nexal inference preflight`
// without --json. It contains nothing the JSON report does not.
func RenderText(r Report) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("AI inference preflight (read-only; nothing was installed or downloaded)")
	w("%s", r.Headline)
	w("")
	w("This Mac and its peers")
	for _, m := range r.Machines {
		who := m.Name
		if m.IsSelf {
			who += " (this Mac)"
		}
		state := "ready to consider"
		if !m.Eligible {
			state = "not used: " + strings.Join(m.Reasons, " ")
		}
		w("  %s: %s, %s, %s total, %s usable for a model (%s); MLX runtime: %s. %s", who, orDash(m.Chip), orDash(m.OS), gibU(m.TotalMemoryBytes), gibU(m.UsableBytes), m.AdmissionSource, strings.ReplaceAll(m.Runtime.State, "_", " "), state)
	}
	if len(r.Links) > 0 {
		w("")
		w("Links from this Mac")
		for _, l := range r.Links {
			bw, lat := "bandwidth not measured", "latency not measured"
			if l.BandwidthMbps != nil {
				bw = fmt.Sprintf("%.0f Mbps", *l.BandwidthMbps)
			}
			if l.LatencyMS != nil {
				lat = fmt.Sprintf("%.0f ms", *l.LatencyMS)
			}
			w("  %s: %s, %s path, %s, %s, post-quantum %s, %d path flaps in the last hour", l.PeerName, l.Quality, l.Path, bw, lat, l.PQ, l.PathFlapsLastHour)
		}
	}
	w("")
	w("Models (sizes are estimates)")
	for _, m := range r.Models {
		w("  %-22s %s", m.DisplayName, m.Summary)
	}
	w("")
	w("Sharing")
	w("  %s", r.Sharing.Statement)
	w("")
	w("Recommended: %s", orDash(r.Recommendation.DisplayName))
	w("  %s", r.Recommendation.Reason)
	for _, l := range r.Recommendation.HowToUse {
		w("  %s", l)
	}
	if len(r.Blockers) > 0 {
		w("")
		w("Things in the way")
		for _, x := range r.Blockers {
			w("  [%s] %s", x.Severity, x.Title)
			w("      %s", x.Detail)
			w("      Fix: %s", x.Fix)
			if x.FixCommand != "" {
				w("      Command: %s", x.FixCommand)
			}
		}
	}
	if len(r.Gaps) > 0 {
		w("")
		w("What this check could not see")
		for _, g := range r.Gaps {
			w("  - %s", g)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
