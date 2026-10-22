package agent

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"nexal/connector/internal/client"
)

func TestErrorText(t *testing.T) {
	if got := errorText(nil); got != "" {
		t.Fatalf("nil error = %q, want empty", got)
	}
	if got := errorText(errors.New("boom")); got != "boom" {
		t.Fatalf("short error = %q", got)
	}
	// Multi-byte runes straddling the 256-byte cut must not leave invalid UTF-8.
	long := errors.New(strings.Repeat("é", 200))
	got := errorText(long)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated text is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Fatalf("missing truncation marker: %q", got)
	}
	if len(got) > 256+len(" …(truncated)") {
		t.Fatalf("not bounded: %d bytes", len(got))
	}
}

func TestAPIErrorShapeAndHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	apiError(rec, 403, "loopback_required")
	if rec.Code != 403 {
		t.Fatalf("status = %d", rec.Code)
	}
	for k, want := range map[string]string{
		"Content-Type":           "application/json",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "loopback_required" || body.Error.Message != "loopback_required" {
		t.Fatalf("body = %+v", body)
	}
}

func TestHeartbeatPayloadHashDetectsChange(t *testing.T) {
	a := client.Heartbeat{Version: "1", OwnerActive: true}
	if heartbeatPayloadHash(a) != heartbeatPayloadHash(a) {
		t.Fatal("hash must be deterministic")
	}
	b := a
	b.OwnerActive = false
	if heartbeatPayloadHash(a) == heartbeatPayloadHash(b) {
		t.Fatal("changed payload should change hash")
	}
}
