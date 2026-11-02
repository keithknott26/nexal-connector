package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An older coordinator rejects unknown fields with 400. ReportWakeInfo must
// retry without lanAddresses, then without lanPrefixes, instead of giving up.
func TestReportWakeInfoDowngradesOnInvalidSchema(t *testing.T) {
	var bodies []map[string]any
	rejectLAN, rejectPrefixes := true, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		_, hasLAN := b["lanAddresses"]
		_, hasPfx := b["lanPrefixes"]
		if (hasLAN && rejectLAN) || (hasPfx && rejectPrefixes) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_schema"}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	w := WakeInfo{
		MACs:         []string{"3c:22:fb:01:02:03"},
		LANKey:       wakeTestKey,
		LANPrefixes:  []string{strings.Repeat("a", 64)},
		LANAddresses: []string{"192.168.1.20"},
	}
	if err := c.ReportWakeInfo(context.Background(), w); err != nil {
		t.Fatalf("ReportWakeInfo = %v; want success after downgrade", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want 3 (full, no lanAddresses, no lanPrefixes)", len(bodies))
	}
	last := bodies[2]
	if _, ok := last["lanAddresses"]; ok {
		t.Errorf("final request still has lanAddresses: %v", last)
	}
	if _, ok := last["lanPrefixes"]; ok {
		t.Errorf("final request still has lanPrefixes: %v", last)
	}
	if last["lanKey"] != wakeTestKey {
		t.Errorf("final request lost lanKey: %v", last)
	}
}

func TestReportWakeInfoRejectsBadLANFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent for invalid wake info")
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	base := WakeInfo{MACs: []string{"3c:22:fb:01:02:03"}, LANKey: wakeTestKey}
	for name, mut := range map[string]func(*WakeInfo){
		"public address":    func(w *WakeInfo) { w.LANAddresses = []string{"8.8.8.8"} },
		"ipv6 address":      func(w *WakeInfo) { w.LANAddresses = []string{"fd00::1"} },
		"non-canonical":     func(w *WakeInfo) { w.LANAddresses = []string{"192.168.001.020"} },
		"garbage":           func(w *WakeInfo) { w.LANAddresses = []string{"nope"} },
		"too many":          func(w *WakeInfo) { w.LANAddresses = make([]string, 9) },
		"bad prefix digest": func(w *WakeInfo) { w.LANPrefixes = []string{"short"} },
	} {
		w := base
		mut(&w)
		if err := c.ReportWakeInfo(context.Background(), w); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
