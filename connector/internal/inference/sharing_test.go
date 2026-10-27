package inference

import (
	"strings"
	"testing"
)

func TestBuildSharingStatements(t *testing.T) {
	cases := []struct {
		name      string
		sharded   bool
		runnable  bool
		single    string
		statement string
	}{
		{"no shard runnable", false, true, "available", statementNoShard},
		{"no shard blocked", false, false, "blocked", statementNoShard},
		{"sharded runnable", true, true, "available", statementShardedFits},
		{"sharded blocked", true, false, "blocked", ""},
	}
	for _, c := range cases {
		s := buildSharing(c.sharded, c.runnable, "")
		if s.SingleHostExecution != c.single || s.MultiHostExecution != "unavailable" {
			t.Errorf("%s: single=%q multi=%q", c.name, s.SingleHostExecution, s.MultiHostExecution)
		}
		if c.statement != "" && s.Statement != c.statement {
			t.Errorf("%s: statement %q", c.name, s.Statement)
		}
		if c.name == "sharded blocked" && !strings.Contains(s.Statement, "blocked until the items below are fixed") {
			t.Errorf("%s: statement %q", c.name, s.Statement)
		}
		if len(s.Evidence) != len(sharingEvidence) {
			t.Errorf("%s: evidence len %d", c.name, len(s.Evidence))
		}
	}
}

func TestBuildSharingEvidenceIsCopiedAndAppended(t *testing.T) {
	s := buildSharing(false, true, "false")
	if len(s.Evidence) != len(sharingEvidence)+1 {
		t.Fatalf("evidence len %d", len(s.Evidence))
	}
	if !strings.Contains(s.Evidence[len(s.Evidence)-1], `distributed_execution="false"`) {
		t.Errorf("last evidence %q", s.Evidence[len(s.Evidence)-1])
	}
	s.Evidence[0] = "mutated"
	if sharingEvidence[0] == "mutated" {
		t.Error("buildSharing aliased the package evidence slice")
	}
}

func TestInstallLineAndHowToUse(t *testing.T) {
	inst := CatalogEntry{ID: "m1", DisplayName: "Model One", Installable: true}
	if l := installLine(inst, "mac"); !strings.Contains(l, "nexal inference install m1") || !strings.Contains(l, "On mac") {
		t.Errorf("installable line %q", l)
	}
	manual := CatalogEntry{ID: "m2", DisplayName: "Model Two"}
	if l := installLine(manual, "mac"); !strings.Contains(l, "Model Two") || strings.Contains(l, "nexal inference install") {
		t.Errorf("manual line %q", l)
	}

	ok := howToUse(inst, "mac", true)
	blocked := howToUse(inst, "mac", false)
	if len(blocked) != len(ok)+1 || !strings.Contains(blocked[1], "not runnable yet") {
		t.Errorf("blocked=%d ok=%d", len(blocked), len(ok))
	}
	if !strings.Contains(ok[len(ok)-1], "Limits:") {
		t.Errorf("last line %q", ok[len(ok)-1])
	}
}
