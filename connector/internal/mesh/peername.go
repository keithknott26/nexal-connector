package mesh

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// maxPeerNameLength keeps the name a single valid DNS label, since the secure
// network derives each peer's private DNS name from it.
const maxPeerNameLength = 63

// PeerName is the name this computer registers with the secure network:
// "<SERIAL>-<Hostname>", e.g. "C02XK1ABCD12-Keiths-Mac-mini".
//
// The hostname alone is not unique: a Mac set up with Migration Assistant keeps
// the old Mac's name, so two different computers showed up as
// "Keiths-MacBook-Pro" and could only be told apart by an auto-added suffix.
// The serial number makes every entry unambiguous in the dashboard; the hostname
// keeps it readable. Either part may be empty; both empty returns "", which
// leaves naming to the runtime's default.
func PeerName(serial, hostname string) string {
	parts := make([]string, 0, 2)
	for _, part := range []string{sanitizeLabel(strings.ToUpper(serial)), sanitizeLabel(hostname)} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	name := strings.Join(parts, "-")
	if len(name) > maxPeerNameLength {
		name = strings.TrimRight(name[:maxPeerNameLength], "-")
	}
	return name
}

// sanitizeLabel keeps ASCII letters and digits, turns every other run of
// characters into one hyphen, and trims hyphens from both ends.
func sanitizeLabel(value string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			hyphen = false
		default:
			if !hyphen && b.Len() > 0 {
				b.WriteByte('-')
				hyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// LocalPeerName reads this Mac's serial number and local hostname. Only fixed
// system tools at absolute paths are run. Any failure degrades to fewer parts.
func LocalPeerName(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	serial := ""
	if out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output(); err == nil {
		serial = platformSerial(out)
	}
	host := ""
	if out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "LocalHostName").Output(); err == nil {
		host = strings.TrimSpace(string(out))
	}
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = strings.TrimSuffix(strings.TrimSuffix(h, ".local"), ".")
		}
	}
	return PeerName(serial, host)
}

// platformSerial extracts IOPlatformSerialNumber from `ioreg -rd1 -c
// IOPlatformExpertDevice` output, e.g. `"IOPlatformSerialNumber" = "C02XK1ABCD12"`.
func platformSerial(out []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"IOPlatformSerialNumber"`) {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			return ""
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}
