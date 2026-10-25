package inference

import (
	"net/http"
	"strings"
	"testing"
)

func TestValidRemoteName(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"", false},
		{".", false},
		{"..", false},
		{"../config.json", false},
		{"a/config.json", false},
		{`a\config.json`, false},
		{"config\x00.json", false},
		{"config..json", false},
		{"notes.txt", false},
		{strings.Repeat("a", 129), false},
	} {
		if got := ValidRemoteName(c.name); got != c.want {
			t.Errorf("ValidRemoteName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStatusErrorCodes(t *testing.T) {
	for _, c := range []struct {
		status int
		code   string
	}{
		{http.StatusNotFound, "file_not_found"},
		{http.StatusUnauthorized, "repo_not_public"},
		{http.StatusForbidden, "repo_not_public"},
		{http.StatusInternalServerError, "http_status"},
		{http.StatusTooManyRequests, "http_status"},
	} {
		if got := statusError(c.status); got.Code != c.code {
			t.Errorf("statusError(%d).Code = %q, want %q", c.status, got.Code, c.code)
		}
	}
}

func TestBuildSharing(t *testing.T) {
	s := buildSharing(false, true, "")
	if s.SingleHostExecution != "available" || s.MultiHostExecution != "unavailable" || s.Statement != statementNoShard {
		t.Errorf("unsharded runnable = %+v", s)
	}
	if got := buildSharing(true, true, "").Statement; got != statementShardedFits {
		t.Errorf("sharded runnable statement = %q", got)
	}
	b := buildSharing(true, false, "")
	if b.SingleHostExecution != "blocked" || !strings.Contains(b.Statement, "blocked") {
		t.Errorf("sharded blocked = %+v", b)
	}
	base := len(sharingEvidence)
	d := buildSharing(false, true, "none")
	if len(d.Evidence) != base+1 || !strings.Contains(d.Evidence[base], `"none"`) {
		t.Errorf("distributed evidence = %v", d.Evidence)
	}
	if len(buildSharing(false, true, "").Evidence) != base {
		t.Errorf("empty distributed must not add evidence")
	}
}
