package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLearningTickScopedProtocol(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer host-secret" {
			t.Error("missing host authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/hosts/host_1/security/learning/events":
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "eventIds": []string{"event_1"}})
		case "/api/hosts/host_1/security/learning/analyze":
			var input map[string]any
			json.NewDecoder(r.Body).Decode(&input)
			if len(input) != 3 || input["escalate"] != true || input["challenge"] != true {
				t.Error("invalid request")
			}
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "id": "run_1", "status": "pending", "trainingEligible": false, "automaticResponseAuthorized": false, "teacher": nil})
		default:
			t.Error("unexpected route")
		}
	}))
	defer server.Close()
	client, err := New(server.URL, "host-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.LearningTick(context.Background(), "host_1"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("missing analysis request")
	}
}
func TestLearningTickRejectsInvalidQueue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "eventIds": []string{"../raw"}})
	}))
	defer server.Close()
	client, _ := New(server.URL, "host-secret", true)
	if client.LearningTick(context.Background(), "host_1") == nil {
		t.Fatal("accepted path as event")
	}
}
