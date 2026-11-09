package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nexal/connector/internal/cybersecurity"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSecurityReportingAcknowledgement(t *testing.T) {
	wrong := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer host-credential" || r.URL.Path != "/api/hosts/host_1/security-events" || r.Method != "POST" {
			t.Error("wrong authority or route")
		}
		var event cybersecurity.Event
		if json.NewDecoder(r.Body).Decode(&event) != nil {
			t.Error("invalid request")
		}
		id := event.EventID
		if wrong {
			id = "other"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "accepted": true, "eventId": id})
	}))
	defer server.Close()
	c, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	e := cybersecurity.Event{SchemaVersion: 1, EventID: "event_1", ObservedAt: time.Now().UTC().Format(cybersecurity.TimeLayout), Kind: "network_alert", Severity: "high", Detector: "public", DetectorVersion: "v1", OriginAssessment: "unknown", EvidenceRef: "ev_1"}
	if err = c.ReportSecurityEvent(context.Background(), "host_1", e); err != nil {
		t.Fatal(err)
	}
	wrong = true
	if c.ReportSecurityEvent(context.Background(), "host_1", e) == nil {
		t.Fatal("wrong acknowledgement accepted")
	}
}

func TestWatermarkStateDoesNotDiscloseDecoyAndChecksAcknowledgement(t *testing.T) {
	wrong := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hosts/host_1/watermark-state" || r.Header.Get("Authorization") != "Bearer host-credential" {
			t.Error("wrong scope")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		for _, key := range []string{"baseline", "observed", "pending", "directory"} {
			if _, ok := body[key]; ok {
				t.Errorf("leaked %s", key)
			}
		}
		at := body["reportedAt"]
		if wrong {
			at = "wrong"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "accepted": true, "reportedAt": at})
	}))
	defer server.Close()
	c, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	state := cybersecurity.CanaryState{Enabled: true, Status: "watching", Baseline: "private-marker", Observed: "private-fingerprint", LastCheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := c.ReportWatermarkState(context.Background(), "host_1", state); err != nil {
		t.Fatal(err)
	}
	wrong = true
	if c.ReportWatermarkState(context.Background(), "host_1", state) == nil {
		t.Fatal("wrong acknowledgement accepted")
	}
}

func TestCanaryIncidentSurvivesBadAcknowledgementAndReachesHandoff(t *testing.T) {
	monitor := cybersecurity.Canary{Directory: t.TempDir()}
	if err := monitor.Configure(true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(monitor.Directory, "security-canary", "nexal-decoy.txt")
	if err := os.WriteFile(path, []byte("controlled integration fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var received []cybersecurity.Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/hosts/host_1/security-events" || r.Header.Get("Authorization") != "Bearer host-credential" {
			t.Error("wrong request scope")
		}
		var event cybersecurity.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		received = append(received, event)
		id := event.EventID
		if len(received) == 1 {
			id = "wrong_ack"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "accepted": true, "eventId": id})
	}))
	defer server.Close()
	api, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	report := func(ctx context.Context, e cybersecurity.Event) error {
		return api.ReportSecurityEvent(ctx, "host_1", e)
	}
	if monitor.Tick(context.Background(), time.Now(), report) == nil {
		t.Fatal("bad ack lost incident")
	}
	monitor = cybersecurity.Canary{Directory: monitor.Directory}
	if err := monitor.Tick(context.Background(), time.Now(), report); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Tick(context.Background(), time.Now(), report); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0] != received[1] {
		t.Fatal("retry not stable or duplicate after ack")
	}
	state, err := monitor.Status()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastEvent == nil || state.LastEvent.EventID != received[0].EventID || state.LastEvent.Detector != "nexal_canary_modified" {
		t.Fatal("missing local handoff evidence")
	}
}

func TestSecurityReportingRejectsBadInputWithoutContactingCoordinator(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	c, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	valid := cybersecurity.Event{SchemaVersion: 1, EventID: "event_1", ObservedAt: time.Now().UTC().Format(cybersecurity.TimeLayout), Kind: "network_alert", Severity: "high", Detector: "public", DetectorVersion: "v1", OriginAssessment: "unknown", EvidenceRef: "ev_1"}
	if c.ReportSecurityEvent(context.Background(), "../bad", valid) == nil {
		t.Fatal("accepted invalid host id")
	}
	if c.ReportSecurityEvent(context.Background(), "host_1", cybersecurity.Event{}) == nil {
		t.Fatal("accepted invalid event")
	}
	if _, err := c.ExchangeWatermarkState(context.Background(), "../bad", cybersecurity.CanaryState{}); err == nil {
		t.Fatal("accepted invalid host id for watermark state")
	}
	if hits != 0 {
		t.Fatalf("coordinator contacted %d times for invalid input", hits)
	}
}

func TestSecurityReportingSurfacesCoordinatorFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	c, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	e := cybersecurity.Event{SchemaVersion: 1, EventID: "event_1", ObservedAt: time.Now().UTC().Format(cybersecurity.TimeLayout), Kind: "network_alert", Severity: "high", Detector: "public", DetectorVersion: "v1", OriginAssessment: "unknown", EvidenceRef: "ev_1"}
	if c.ReportSecurityEvent(context.Background(), "host_1", e) == nil {
		t.Fatal("coordinator failure swallowed for security event")
	}
	if c.ReportWatermarkState(context.Background(), "host_1", cybersecurity.CanaryState{Enabled: true, Status: "watching"}) == nil {
		t.Fatal("coordinator failure swallowed for watermark state")
	}
}

func TestWatermarkStateOmitsAlreadyReportedOperationResult(t *testing.T) {
	var sawResult []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		_, has := body["operationResult"]
		sawResult = append(sawResult, has)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "accepted": true, "reportedAt": body["reportedAt"], "operation": map[string]any{"id": "op_1", "kind": "regenerate", "expiresAt": "later"}})
	}))
	defer server.Close()
	c, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	state := cybersecurity.CanaryState{Enabled: true, Status: "watching", OperationResult: &cybersecurity.WatermarkResult{ID: "op_1", Kind: "regenerate"}}
	op, err := c.ExchangeWatermarkState(context.Background(), "host_1", state)
	if err != nil || op == nil || op.ID != "op_1" {
		t.Fatalf("operation not returned: %v %+v", err, op)
	}
	state.OperationReported = true
	if _, err = c.ExchangeWatermarkState(context.Background(), "host_1", state); err != nil {
		t.Fatal(err)
	}
	if len(sawResult) != 2 || !sawResult[0] || sawResult[1] {
		t.Fatalf("operationResult presence = %v, want [true false]", sawResult)
	}
}
