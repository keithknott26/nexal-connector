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
	if err = c.ValidationTick(context.Background(), "host_1", cybersecurity.Canary{Directory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("result was not delivered")
	}
}

// validationServer serves the validation routes for one lease. Each field tweaks
// one coordinator answer so a test can exercise a single refusal path.
type validationServer struct {
	run          any
	tickVersion  int
	authorized   bool
	completeAck  map[string]any
	completeBody struct {
		Executed bool   `json:"executed"`
		Reason   string `json:"reason"`
	}
	completes int
}

func (v *validationServer) start(t *testing.T, id string) *Client {
	t.Helper()
	base := "/api/hosts/host_1/security/validation"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case base:
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": v.tickVersion, "run": v.run})
		case base + "/" + id:
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "authorized": v.authorized})
		case "/api/hosts/host_1/security-events":
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "accepted": true, "eventId": "e"})
		case base + "/" + id + "/complete":
			v.completes++
			json.NewDecoder(req.Body).Decode(&v.completeBody)
			json.NewEncoder(w).Encode(v.completeAck)
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	c, err := New(server.URL, "host-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidationTickRefusals(t *testing.T) {
	run := cybersecurity.ValidationRun{ID: uuid.NewString(), Nonce: uuid.NewString(), Module: "canary_integrity", ExpiresAt: time.Now().Add(time.Minute)}
	okAck := map[string]any{"schemaVersion": 1, "accepted": true}
	canary := cybersecurity.Canary{Directory: t.TempDir()}

	t.Run("invalid host never calls the coordinator", func(t *testing.T) {
		c, err := New("http://127.0.0.1:1", "host-credential", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.ValidationTick(context.Background(), "../etc", canary); err == nil {
			t.Fatal("invalid host accepted")
		}
	})
	t.Run("unknown response schema", func(t *testing.T) {
		v := &validationServer{run: run, tickVersion: 2, authorized: true, completeAck: okAck}
		c := v.start(t, run.ID)
		if err := c.ValidationTick(context.Background(), "host_1", canary); err == nil || v.completes != 0 {
			t.Fatalf("err=%v completes=%d", err, v.completes)
		}
	})
	t.Run("no lease runs nothing", func(t *testing.T) {
		v := &validationServer{run: nil, tickVersion: 1, authorized: true, completeAck: okAck}
		c := v.start(t, run.ID)
		if err := c.ValidationTick(context.Background(), "host_1", canary); err != nil || v.completes != 0 {
			t.Fatalf("err=%v completes=%d", err, v.completes)
		}
	})
	t.Run("expired lease is rejected", func(t *testing.T) {
		expired := run
		expired.ExpiresAt = time.Now().Add(-time.Minute)
		v := &validationServer{run: expired, tickVersion: 1, authorized: true, completeAck: okAck}
		c := v.start(t, run.ID)
		if err := c.ValidationTick(context.Background(), "host_1", canary); err == nil || v.completes != 0 {
			t.Fatalf("err=%v completes=%d", err, v.completes)
		}
	})
	t.Run("revoked lease is not executed or completed", func(t *testing.T) {
		v := &validationServer{run: run, tickVersion: 1, authorized: false, completeAck: okAck}
		c := v.start(t, run.ID)
		if err := c.ValidationTick(context.Background(), "host_1", canary); err == nil || v.completes != 0 {
			t.Fatalf("err=%v completes=%d", err, v.completes)
		}
	})
	t.Run("unaccepted completion acknowledgement is an error", func(t *testing.T) {
		v := &validationServer{run: run, tickVersion: 1, authorized: true, completeAck: map[string]any{"schemaVersion": 1, "accepted": false}}
		c := v.start(t, run.ID)
		if err := c.ValidationTick(context.Background(), "host_1", canary); err == nil || v.completes != 1 {
			t.Fatalf("err=%v completes=%d", err, v.completes)
		}
	})
}
