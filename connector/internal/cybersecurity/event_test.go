package cybersecurity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizePrivacyAndRetry(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	input := []byte(`{"event_type":"alert","timestamp":"2026-09-25T12:00:00.000000+0000","src_ip":"10.0.0.1","payload":"SECRET","alert":{"signature_id":123,"severity":1,"signature":"SECRET"}}`)
	a, err := NormalizeEVE(input, "capture_1", 1, "public_1", now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NormalizeEVE(input, "capture_1", 1, "public_1", now)
	if *a != *b {
		t.Fatal("retry identity changed")
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "10.0.0.1") {
		t.Fatal("content escaped")
	}
	if a.Severity != "high" || a.OriginAssessment != "unknown" {
		t.Fatal(a)
	}
	a.Kind = "ai_code_signal"
	a.OriginAssessment = "suspected"
	if a.Validate(now) == nil {
		t.Fatal("authorship escalated")
	}
	a.Severity = "info"
	if a.Validate(now) != nil {
		t.Fatal("valid authorship rejected")
	}
	a.ObservedAt = now.Add(-8 * 24 * time.Hour).Format(TimeLayout)
	if a.Validate(now) == nil {
		t.Fatal("stale event accepted")
	}
}
func TestRejectMalformed(t *testing.T) {
	for _, line := range []string{`{`, `{} {}`, `{"event_type":"alert"}`, strings.Repeat("x", MaxEVEBytes+1)} {
		if _, err := NormalizeEVE([]byte(line), "capture", 1, "public_1", time.Now()); err == nil {
			t.Fatal("accepted malformed input")
		}
	}
	e, err := NormalizeEVE([]byte(`{"event_type":"flow"}`), "capture", 1, "public_1", time.Now())
	if e != nil || err != nil {
		t.Fatal("non-alert not skipped")
	}
}
func FuzzNormalizeEVE(f *testing.F) {
	f.Add([]byte(`{"event_type":"flow"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := NormalizeEVE(b, "capture", 1, "public_1", time.Now())
		if err == nil && e != nil && e.Validate(time.Now()) != nil {
			t.Fatal("invalid event")
		}
	})
}

func TestScannerEvidenceContract(t *testing.T) {
	now := time.Now()
	score := 0.4
	e := scannerEvent(now, "code_style_signal", "info", "nexal_style", "style_baseline_deviation", strings.Repeat("a", 64), strings.Repeat("b", 64), "observation")
	e.OriginAssessment = "insufficient_evidence"
	e.Evidence = &Evidence{Engine: "nexal_style", RuleID: "style_baseline_deviation", RulesVersion: StyleVersion, ContentSHA256: strings.Repeat("a", 64), BaselineID: strings.Repeat("b", 64), Score: &score}
	if err := e.Validate(now); err != nil {
		t.Fatal(err)
	}
	e.Severity = "high"
	if e.Validate(now) == nil {
		t.Fatal("style signal escalated to threat")
	}
	e.Severity = "info"
	e.OriginAssessment = "suspected"
	if e.Validate(now) == nil {
		t.Fatal("style attributed AI origin")
	}
	e.OriginAssessment = "insufficient_evidence"
	e.Evidence.RuleID = "/Users/private/script.py"
	if e.Validate(now) == nil {
		t.Fatal("file path escaped evidence validator")
	}
	e.Evidence.RuleID = "style_baseline_deviation"
	score = 2
	if e.Validate(now) == nil {
		t.Fatal("unbounded score")
	}
}
