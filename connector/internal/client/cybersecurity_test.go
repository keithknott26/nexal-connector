package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nexal/connector/internal/cybersecurity"
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
