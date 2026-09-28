package client

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"nexal/connector/internal/cybersecurity"
	"testing"
	"time"
)

func TestValidationLeaseToCorrelatedReport(t *testing.T) {
	r := cybersecurity.ValidationRun{ID: uuid.NewString(), Nonce: uuid.NewString(), Module: "canary_integrity", ExpiresAt: time.Now().Add(time.Minute)}
	base := "/api/hosts/host_1/security/validation"
	reported := false
	completed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("Authorization") != "Bearer host-credential" {
			t.Error("wrong authority")
		}
		switch req.URL.Path {
		case base:
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "run": r})
		case base + "/" + r.ID:
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "authorized": true})
		case "/api/hosts/host_1/security-events":
			var e cybersecurity.Event
			json.NewDecoder(req.Body).Decode(&e)
			if e.EvidenceRef != "validation_"+r.ID {
				t.Error("missing correlation")
			}
			reported = true
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "accepted": true, "eventId": e.EventID})
		case base + "/" + r.ID + "/complete":
			var b struct {
				Executed bool   `json:"executed"`
				Reason   string `json:"reason"`
			}
			json.NewDecoder(req.Body).Decode(&b)
			if !reported || !b.Executed || b.Reason != "completed" {
				t.Error("unproven result")
			}
			completed = true
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "accepted": true})
		default:
			t.Error("unexpected route")
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	c, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ValidationTick(context.Background(), "host_1", cybersecurity.Canary{Directory: t.TempDir()}, cybersecurity.Scanner{}); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("result was not delivered")
	}
}
