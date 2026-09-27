package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nexal/connector/internal/observability"
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

// postQuantumKeyExchange reports whether a negotiated group is one of Go's hybrid
// ML-KEM key exchanges. Observing our own handshake is a self-report about this
// process, not attestation of anything the peer claims.
func postQuantumKeyExchange(id tls.CurveID) bool {
	switch id { // gitleaks:allow -- public algorithm names, not key material
	// tls.MLKEM1024 is omitted: the constant needs a go1.27 module directive, and
	// it is a standalone ML-KEM group Go never offers by default anyway.
	case tls.X25519MLKEM768, tls.SecP256r1MLKEM768, tls.SecP384r1MLKEM1024:
		return true
	}
	return false
}

// The coordinator transport must actually negotiate a post-quantum group. Go
// enables the hybrid ML-KEM groups only for a nil CurvePreferences, and the
// GODEBUG that gates them is selected by go.mod's go directive, so both "pinning"
// the curve and lowering that directive downgrade the handshake with no visible
// change. This test is what makes either downgrade fail instead of pass quietly.
func TestCoordinatorHandshakeIsTLS13AndPostQuantum(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ioJSON(w, map[string]any{"attempt": nil})
	}))
	defer srv.Close()
	c, err := New(srv.URL, "host-secret-secret", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := observability.Underlying(c.http.Transport).(*http.Transport).TLSClientConfig
	if cfg.CurvePreferences != nil {
		t.Fatal("CurvePreferences is set: Go then offers only the listed groups, which silently drops post-quantum key agreement")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("coordinator TLS floor is not 1.3: %#x", cfg.MinVersion)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("certificate verification disabled")
	}
	var state tls.ConnectionState
	// Observe the completed handshake of the production configuration; trust only
	// this test server's certificate, and change nothing else about the config.
	cfg.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		state = cs
		return nil
	}
	if _, err := c.Next(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	if state.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS version %#x", state.Version)
	}
	if !postQuantumKeyExchange(state.CurveID) {
		t.Fatalf("classical key exchange negotiated: %v (%d)", state.CurveID, state.CurveID)
	}
	t.Logf("negotiated %v (%d) at TLS %#x", state.CurveID, state.CurveID, state.Version)
}
