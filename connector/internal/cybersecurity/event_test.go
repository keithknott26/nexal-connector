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

func TestValidateEnumsAndAuthorship(t *testing.T) {
	now := time.Now().UTC()
	base := func() Event {
		return Event{SchemaVersion: 1, EventID: "evt_1", ObservedAt: now.Format(TimeLayout), Kind: "network_alert", Severity: "high",
			Detector: "suricata", DetectorVersion: "1", OriginAssessment: "unknown", EvidenceRef: "ref_1"}
	}
	if err := base().Validate(now); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Event){
		"schema version":             func(e *Event) { e.SchemaVersion = 2 },
		"unknown kind":               func(e *Event) { e.Kind = "bogus" },
		"unknown severity":           func(e *Event) { e.Severity = "urgent" },
		"unknown origin":             func(e *Event) { e.OriginAssessment = "guilty" },
		"threat with origin verdict": func(e *Event) { e.OriginAssessment = "suspected" },
		"origin signal above info":   func(e *Event) { e.Kind = "ai_code_signal"; e.Severity = "high" },
		"watermark cannot be suspect": func(e *Event) {
			e.Kind = "text_watermark_signal"
			e.Severity = "info"
			e.OriginAssessment = "suspected"
		},
		"future observation": func(e *Event) { e.ObservedAt = now.Add(time.Hour).Format(TimeLayout) },
		"stale observation":  func(e *Event) { e.ObservedAt = now.Add(-8 * 24 * time.Hour).Format(TimeLayout) },
	} {
		e := base()
		mutate(&e)
		if e.Validate(now) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	e := base()
	e.Kind, e.Severity, e.OriginAssessment = "ai_code_signal", "info", "suspected"
	if err := e.Validate(now); err != nil {
		t.Errorf("info-level origin signal rejected: %v", err)
	}
}
