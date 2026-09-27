// Package cybersecurity defines public, payload-free telemetry contracts.
// Proprietary classifiers, watermark keys, prompts and weights stay server-side.
package cybersecurity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"time"
)

const MaxEventBytes = 16 << 10
const MaxEVEBytes = 256 << 10
const TimeLayout = "2006-01-02T15:04:05.000Z"

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type Event struct {
	SchemaVersion    int       `json:"schemaVersion"`
	EventID          string    `json:"eventId"`
	ObservedAt       string    `json:"observedAt"`
	Kind             string    `json:"kind"`
	Severity         string    `json:"severity"`
	Detector         string    `json:"detector"`
	DetectorVersion  string    `json:"detectorVersion"`
	OriginAssessment string    `json:"originAssessment"`
	EvidenceRef      string    `json:"evidenceRef"`
	Evidence         *Evidence `json:"evidence,omitempty"`
}

// Evidence contains identifiers and numerical summaries only, never file paths,
// source bytes, matched strings, or claims of AI authorship.
type Evidence struct {
	Engine        string   `json:"engine"`
	RuleID        string   `json:"ruleId"`
	RulesVersion  string   `json:"rulesVersion"`
	ContentSHA256 string   `json:"contentSha256"`
	BaselineID    string   `json:"baselineId,omitempty"`
	Score         *float64 `json:"score,omitempty"`
	TestOnly      bool     `json:"testOnly,omitempty"`
}

var digestID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func contains(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func (e Event) Validate(now time.Time) error {
	for _, field := range []struct {
		value string
		max   int
	}{{e.EventID, 80}, {e.Detector, 80}, {e.DetectorVersion, 40}, {e.EvidenceRef, 80}} {
		if len(field.value) > field.max || !identifier.MatchString(field.value) {
			return errors.New("invalid security identifier")
		}
	}

	at, err := time.Parse(TimeLayout, e.ObservedAt)
	if err != nil || at.UTC().Format(TimeLayout) != e.ObservedAt || at.After(now.Add(time.Minute)) || at.Before(now.Add(-7*24*time.Hour)) {
		return errors.New("invalid security observation time")
	}
	if e.SchemaVersion != 1 || !contains(e.Kind, "network_alert", "behavior_alert", "ai_code_signal", "text_watermark_signal", "agent_policy_violation", "sensor_health", "code_style_signal") ||
		!contains(e.Severity, "info", "low", "medium", "high", "critical") || !contains(e.OriginAssessment, "unknown", "suspected", "watermark_detected", "unsupported", "insufficient_evidence") {
		return errors.New("invalid security event")
	}
	origin := e.Kind == "ai_code_signal" || e.Kind == "text_watermark_signal"
	if origin && e.Severity != "info" || !origin && e.Kind != "code_style_signal" && e.OriginAssessment != "unknown" || e.Kind == "text_watermark_signal" && e.OriginAssessment == "suspected" {
		return errors.New("authorship is not a threat verdict")
	}
	if e.Kind == "code_style_signal" && (e.Severity != "info" || e.OriginAssessment != "insufficient_evidence" || e.Evidence == nil || e.Evidence.Engine != "nexal_style") {
		return errors.New("style is not a threat or authorship verdict")
	}
	if v := e.Evidence; v != nil {
		if !contains(v.Engine, "yara_x", "nexal_style") || !identifier.MatchString(v.RuleID) || len(v.RuleID) > 80 || !identifier.MatchString(v.RulesVersion) || len(v.RulesVersion) > 40 || !digestID.MatchString(v.ContentSHA256) {
			return errors.New("invalid detector evidence")
		}
		if v.Score != nil && (math.IsNaN(*v.Score) || math.IsInf(*v.Score, 0) || *v.Score < 0 || *v.Score > 1) {
			return errors.New("invalid detector score")
		}
		if v.Engine == "nexal_style" && (e.Kind != "code_style_signal" || !digestID.MatchString(v.BaselineID) || v.Score == nil || v.TestOnly) {
			return errors.New("invalid style evidence")
		}
		if v.Engine == "yara_x" && (e.Kind != "behavior_alert" || v.BaselineID != "" || v.Score != nil || v.TestOnly && e.Severity != "info") {
			return errors.New("invalid byte evidence")
		}
	}
	return nil
}

// NormalizeEVE retains no packet, IP, path, signature text, hostname, or flow ID.
// sourceID must be a persisted opaque capture UUID, never a path; offset is the
// persisted line number. Their digest gives stable retry IDs within that capture.
func NormalizeEVE(line []byte, sourceID string, offset uint64, rulesVersion string, now time.Time) (*Event, error) {
	if len(line) > MaxEVEBytes || !identifier.MatchString(sourceID) || len(sourceID) > 80 || offset == 0 {
		return nil, errors.New("invalid EVE input")
	}
	var input struct {
		EventType string `json:"event_type"`
		Timestamp string `json:"timestamp"`
		Alert     *struct {
			SignatureID uint64 `json:"signature_id"`
			Severity    int    `json:"severity"`
		} `json:"alert"`
	}
	d := json.NewDecoder(bytes.NewReader(line))
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid EVE JSON")
	}
	if input.EventType != "alert" {
		return nil, nil
	}
	if input.Alert == nil || input.Alert.SignatureID == 0 || input.Alert.Severity < 1 || input.Alert.Severity > 3 {
		return nil, errors.New("invalid EVE alert")
	}
	at, err := time.Parse(time.RFC3339Nano, input.Timestamp)
	if err != nil {
		at, err = time.Parse("2006-01-02T15:04:05.999999999-0700", input.Timestamp)
	}
	if err != nil {
		return nil, errors.New("invalid EVE timestamp")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", sourceID, offset)))
	ref := hex.EncodeToString(digest[:])
	e := Event{1, "eve_" + ref, at.UTC().Format(TimeLayout), "network_alert", map[int]string{1: "high", 2: "medium", 3: "low"}[input.Alert.Severity], fmt.Sprintf("suricata_%d", input.Alert.SignatureID), rulesVersion, "unknown", "ev_" + ref, nil}
	if err := e.Validate(now); err != nil {
		return nil, err
	}
	return &e, nil
}
