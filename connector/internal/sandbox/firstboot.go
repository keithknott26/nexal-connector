package sandbox

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"
)

var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}=?$`)

// ParseFirstBootLine parses one console line. ok is true for a well-formed
// success report; failed is true (with reason) for a failure report. Console
// output is attacker-influenced (the guest is untrusted), so every field is
// validated. It is pure.
func ParseFirstBootLine(line string) (fb FirstBoot, ok bool, failed bool, reason string) {
	line = strings.TrimSpace(strings.TrimRight(line, "\r"))
	if i := strings.Index(line, FirstBootFailedPrefix); i >= 0 {
		r := strings.TrimSpace(line[i+len(FirstBootFailedPrefix):])
		if len(r) > 200 {
			r = strings.ToValidUTF8(r[:200], "")
		}
		return FirstBoot{}, false, true, r
	}
	i := strings.Index(line, FirstBootPrefix)
	if i < 0 {
		return FirstBoot{}, false, false, ""
	}
	if err := json.Unmarshal([]byte(line[i+len(FirstBootPrefix):]), &fb); err != nil {
		return FirstBoot{}, false, false, ""
	}
	if _, err := netip.ParseAddr(fb.MeshIP); err != nil {
		return FirstBoot{}, false, false, ""
	}
	if fb.HostKeyFingerprint != "" && !fingerprintPattern.MatchString(fb.HostKeyFingerprint) {
		return FirstBoot{}, false, false, ""
	}
	return fb, true, false, ""
}

// scanFirstBoot looks through console text (newest line wins) for a report.
func scanFirstBoot(text string) (fb FirstBoot, ok bool, failed bool, reason string) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if f, o, fl, r := ParseFirstBootLine(lines[i]); o || fl {
			return f, o, fl, r
		}
	}
	return FirstBoot{}, false, false, ""
}
