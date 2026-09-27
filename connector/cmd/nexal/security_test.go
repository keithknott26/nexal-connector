package main

import (
	"context"
	"encoding/json"
	"nexal/connector/internal/cybersecurity"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecurityRequiresExplicitBaselineApprovalAndRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, args := range [][]string{
		{"baseline", "--config", path},
		{"configure", "--config", path},
		{"scan", "--config", path},
		{"unknown", "--config", path},
	} {
		if err := securityCommand(context.Background(), args); err == nil {
			t.Fatalf("accepted unsafe or incomplete command %v", args)
		}
	}
}

func TestLocalFindingsResponseFitsNativeTransport(t *testing.T) {
	findings := make([]cybersecurity.LocalFinding, 100)
	for i := range findings {
		findings[i] = cybersecurity.LocalFinding{Path: strings.Repeat("x", 4096), EventID: "test"}
	}
	response := boundedLocalFindings(findings)
	raw, _ := json.Marshal(response)
	if !response.Truncated || len(response.Findings) == 0 || len(raw) > 60<<10 {
		t.Fatal("unbounded native response", len(raw), len(response.Findings))
	}
}
