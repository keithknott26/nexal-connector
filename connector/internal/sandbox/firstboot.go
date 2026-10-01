package sandbox

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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
	if fb.HostKey != "" {
		key, fp, ok := normalizeHostKey(fb.HostKey)
		if !ok || (fb.HostKeyFingerprint != "" && fb.HostKeyFingerprint != fp) {
			return FirstBoot{}, false, false, ""
		}
		fb.HostKey = key
		if fb.HostKeyFingerprint == "" {
			fb.HostKeyFingerprint = fp
		}
	}
	return fb, true, false, ""
}

// normalizeHostKey accepts "ssh-ed25519 AAAA..." (an optional trailing comment is
// dropped) and returns the comment-free key and its OpenSSH SHA256 fingerprint
// (unpadded base64). The blob must decode and carry the ssh-ed25519 key type.
func normalizeHostKey(s string) (key, fingerprint string, ok bool) {
	f := strings.Fields(s)
	if len(f) < 2 || f[0] != "ssh-ed25519" || len(f[1]) > 512 {
		return "", "", false
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) < 4 {
		return "", "", false
	}
	n := int(binary.BigEndian.Uint32(blob))
	if n != len("ssh-ed25519") || len(blob) < 4+n || string(blob[4:4+n]) != "ssh-ed25519" {
		return "", "", false
	}
	sum := sha256.Sum256(blob)
	return f[0] + " " + f[1], "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), true
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
