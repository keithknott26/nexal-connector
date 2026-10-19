package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidSharingState(t *testing.T) {
	for _, s := range []string{SharingEnabled, SharingDeclined, SharingFailed, SharingAlreadyOn} {
		if !ValidSharingState(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "Enabled", "on", "already-on"} {
		if ValidSharingState(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestCleanSharingDetail(t *testing.T) {
	if got := CleanSharingDetail("  line1\nline2\x00\x7f\ttab  "); got != "line1 line2   tab" {
		t.Fatalf("control characters not scrubbed: %q", got)
	}
	// A multi-byte rune straddling the cap must be dropped whole, never split.
	long := strings.Repeat("a", MaxSharingDetail-1) + "é" + "tail"
	got := CleanSharingDetail(long)
	if len(got) > MaxSharingDetail || !utf8.ValidString(got) {
		t.Fatalf("bad truncation: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if got != strings.Repeat("a", MaxSharingDetail-1) {
		t.Fatalf("unexpected truncation result length %d", len(got))
	}
}

func TestReportSharingResult(t *testing.T) {
	var method, path, auth string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReportSharingResult(context.Background(), "req_1", SharingFailed, "bad\nthing"); err != nil {
		t.Fatal(err)
	}
	if method != "POST" || path != "/api/v2/hosts/sharing-requests/req_1" || auth != "Bearer tok" {
		t.Fatalf("wrong request: %s %s %q", method, path, auth)
	}
	if body["state"] != "failed" || body["detail"] != "bad thing" {
		t.Fatalf("wrong body: %v", body)
	}
}

func TestReportSharingResultRejectsBadInputBeforeNetwork(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.ReportSharingResult(ctx, "../etc", SharingEnabled, ""); err == nil {
		t.Error("invalid request id accepted")
	}
	if err := c.ReportSharingResult(ctx, "req_1", "maybe", ""); err == nil {
		t.Error("invalid state accepted")
	}
	if hit {
		t.Fatal("network was called for invalid input")
	}
}
