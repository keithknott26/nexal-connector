package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostRESTContract(t *testing.T) {
	var routes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes = append(routes, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer host-scoped-secret" {
			t.Error("missing host auth")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/hosts/h1/heartbeat":
			ioJSON(w, map[string]any{"ok": true, "leaseSeconds": 60})
		case "/api/hosts/h1/next":
			ioJSON(w, map[string]any{"attempt": Attempt{ID: "a1", JobID: "j1", HostID: "h1", Template: "monte-carlo-pi-v1", Samples: 100, LeaseExpiresAt: time.Now().Add(time.Minute)}})
		case "/api/attempts/a1/heartbeat":
			ioJSON(w, Renewal{OK: true, LeaseExpiresAt: time.Now().Add(time.Minute)})
		case "/api/attempts/a1/complete":
			ioJSON(w, map[string]bool{"accepted": true})
		default:
			t.Error("unexpected route")
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, "host-scoped-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Heartbeat(ctx, "h1", Heartbeat{OwnerActive: false}); err != nil {
		t.Fatal(err)
	}
	at, err := c.Next(ctx, "h1")
	if err != nil || at.ID != "a1" {
		t.Fatal(err)
	}
	if _, err := c.Renew(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := c.Complete(ctx, "a1", Result{Samples: 100, Inside: 80, Pi: 3.2}, 1); err != nil || !ok {
		t.Fatal(err)
	}
	if len(routes) != 4 {
		t.Fatal(routes)
	}
}
func ioJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
func TestNoRedirectCredentialLeak(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer srv.Close()
	c, _ := New(srv.URL, "secret-secret-secret", true)
	if _, err := c.Next(context.Background(), "h1"); err == nil {
		t.Fatal("redirect accepted")
	}
	if hit {
		t.Fatal("redirect target reached")
	}
}
func TestRejectOversizeUnknownFieldsAndPaths(t *testing.T) {
	body := `{"attempt":null,"runShell":"evil"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	c, _ := New(srv.URL, "host-secret-secret", true)
	if _, err := c.Next(context.Background(), "h1"); err == nil {
		t.Fatal("unknown schema accepted")
	}
	body = strings.Repeat(" ", 65537)
	if _, err := c.Next(context.Background(), "h1"); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := c.Next(context.Background(), "../admin"); err == nil {
		t.Fatal("path injection accepted")
	}
}

func TestProductionTLSNeverSkipsCertificateVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ioJSON(w, map[string]any{"attempt": nil}) }))
	defer srv.Close()
	c, err := New(srv.URL, "host-secret-secret", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Next(context.Background(), "h1"); err == nil {
		t.Fatal("untrusted production TLS certificate accepted")
	}
}
