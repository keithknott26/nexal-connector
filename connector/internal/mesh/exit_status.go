package mesh

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

const maxExitStatusBytes = 64 << 10

type routeOutput struct{ bytes.Buffer }

func (b *routeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxExitStatusBytes {
		return 0, errors.New("route status exceeds limit")
	}
	return b.Buffer.Write(p)
}

// ReadExitNodeStatus reads the existing local runtime, without selecting routes,
// administrator rights, inherited PATH lookup, or exporting routing identifiers.
// "selected" means the runtime selected a default route, not verified egress.
func ReadExitNodeStatus(ctx context.Context) string {
	path, err := trustedExecutable("nexal-network")
	if err != nil {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "networks", "ls")
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	out := &routeOutput{}
	// Cobra's human-readable output can be written to stderr. Both streams use
	// one bounded writer; os/exec serializes writes when these are the same writer.
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return "unknown"
	}
	return parseExitNodeStatus(out.Bytes())
}

// The installed runtime has no JSON networks-list option. Recognize its complete
// explicit records; format changes, ambiguous records and truncated output must
// never be presented as a confirmed route selection or absence.
func parseExitNodeStatus(out []byte) string {
	if len(out) > maxExitStatusBytes {
		return "unknown"
	}
	text := strings.TrimSpace(string(out))
	if text == "No networks available." {
		return "unavailable"
	}
	if !strings.HasPrefix(text, "Available Networks:\n") {
		return "unknown"
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	type record struct{ id, network, domains, status string }
	var current *record
	selected, offered, count := false, false, 0
	flush := func() bool {
		if current == nil {
			return true
		}
		if current.id == "" || (current.network == "" && current.domains == "") || (current.status != "Selected" && current.status != "Not Selected") {
			return false
		}
		count++
		if current.network == "0.0.0.0/0" || current.network == "::/0" {
			offered = true
			selected = selected || current.status == "Selected"
		}
		return true
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "Available Networks:" {
			continue
		}
		if strings.HasPrefix(line, "- ID: ") {
			if !flush() {
				return "unknown"
			}
			current = &record{id: strings.TrimSpace(strings.TrimPrefix(line, "- ID: "))}
			continue
		}
		if current == nil {
			return "unknown"
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return "unknown"
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Network":
			if current.network != "" {
				return "unknown"
			}
			current.network = value
		case "Domains":
			if current.domains != "" {
				return "unknown"
			}
			current.domains = value
		case "Status":
			if current.status != "" {
				return "unknown"
			}
			current.status = value
		case "Resolved IPs":
		default:
			// The only variable keys emitted by the supported format are resolved
			// domain entries, and they cannot define route status or default prefixes.
			if current.domains == "" || !strings.HasPrefix(key, "[") || !strings.HasSuffix(key, "]") {
				return "unknown"
			}
		}
	}
	if scanner.Err() != nil || !flush() || count == 0 {
		return "unknown"
	}
	if selected {
		return "selected"
	}
	if offered {
		return "not_selected"
	}
	return "unavailable"
}
