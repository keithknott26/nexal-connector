package mesh

import (
	"strings"
	"testing"
)

func TestExitNodeStatusRequiresExplicitDefaultRouteSelection(t *testing.T) {
	tests := []struct{ name, text, want string }{
		{"empty", "No networks available.\n", "unavailable"},
		{"selected", "Available Networks:\n\n  - ID: nexal-exit\n    Network: 0.0.0.0/0\n    Status: Selected\n", "selected"},
		{"ipv6", "Available Networks:\n\n  - ID: private_exit\n    Network: ::/0\n    Status: Selected\n", "selected"},
		{"not selected", "Available Networks:\n  - ID: nexal-exit\n    Network: 0.0.0.0/0\n    Status: Not Selected\n", "not_selected"},
		{"named like exit is insufficient", "Available Networks:\n  - ID: nexal-exit\n    Network: 192.168.1.0/24\n    Status: Selected\n", "unavailable"},
		{"truncated", "Available Networks:\n  - ID: nexal-exit\n    Network: 0.0.0.0/0\n", "unknown"},
		{"ambiguous", "Available Networks:\n  - ID: nexal-exit\n    Network: 0.0.0.0/0\n    Status: Selected\n    Status: Not Selected\n", "unknown"},
		{"unrecognized", "Available Networks:\n  - ID: nexal-exit\n    Network: 0.0.0.0/0\n    Status: Pending\n", "unknown"},
		{"daemon error", "failed to connect to daemon", "unknown"},
		{"oversize", strings.Repeat("x", maxExitStatusBytes+1), "unknown"},
		{"domain records", "Available Networks:\n  - ID: website\n    Domains: example.test\n    Status: Selected\n    Resolved IPs:\n      [example.test]: 100.70.1.1\n  - ID: default\n    Network: 0.0.0.0/0\n    Status: Not Selected\n", "not_selected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseExitNodeStatus([]byte(tt.text)); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
