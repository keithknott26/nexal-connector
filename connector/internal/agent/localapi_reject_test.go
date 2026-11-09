package agent

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalAPIRejectionBranches(t *testing.T) {
	a, _ := testAgent(t)
	token := strings.Repeat("x", 64)
	h, err := a.Handler(token)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, method, url, body, ctype, host string
		code                                 int
		errCode                              string
	}{
		{"query string", "GET", "http://127.0.0.1:8788/v1/status?x=1", "", "", "", 400, "query_not_allowed"},
		{"status with body", "GET", "http://127.0.0.1:8788/v1/status", "{}", "", "", 400, "body_not_allowed"},
		{"peers with body", "GET", "http://127.0.0.1:8788/v1/peers", "{}", "", "", 400, "body_not_allowed"},
		{"policy get with body", "GET", "http://127.0.0.1:8788/v1/policy", "{}", "", "", 400, "body_not_allowed"},
		{"policy wrong media type", "PUT", "http://127.0.0.1:8788/v1/policy", "{}", "text/plain", "", 415, "json_required"},
		{"policy invalid body", "PUT", "http://127.0.0.1:8788/v1/policy", "not json", "application/json", "", 400, "invalid_resource_policy"},
		{"policy bad method", "DELETE", "http://127.0.0.1:8788/v1/policy", "", "", "", 405, "method_not_allowed"},
		{"status wrong method", "PUT", "http://127.0.0.1:8788/v1/status", "", "", "", 405, "method_not_allowed"},
		{"unknown command", "POST", "http://127.0.0.1:8788/v1/nope", "", "", "", 404, "not_found"},
		{"jobs prefix blocked", "GET", "http://127.0.0.1:8788/v1/jobs/1", "", "", "", 403, "verified_tunnel_dispatch_not_integrated"},
		{"non-loopback host", "GET", "http://example.test:8788/v1/status", "", "", "example.test:8788", 403, "local_nonbrowser_client_required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.url, strings.NewReader(c.body))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+token)
			if c.ctype != "" {
				r.Header.Set("Content-Type", c.ctype)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.code || !strings.Contains(w.Body.String(), `"code":"`+c.errCode+`"`) {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), c.code, c.errCode)
			}
		})
	}
}

func TestLocalAPIHandlerRequiresStrongToken(t *testing.T) {
	a, _ := testAgent(t)
	for _, tok := range []string{"", strings.Repeat("x", 31), strings.Repeat("x", 4097)} {
		if _, err := a.Handler(tok); err == nil {
			t.Errorf("token of length %d accepted", len(tok))
		}
	}
}

// A wrong method on /v1/policy must say which methods are allowed, and a
// request from a non-loopback peer is refused before the token is even looked at.
func TestLocalAPIPolicyAllowHeaderAndNonLoopbackPeer(t *testing.T) {
	a, _ := testAgent(t)
	token := strings.Repeat("x", 64)
	h, err := a.Handler(token)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("DELETE", "http://127.0.0.1:8788/v1/policy", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 || w.Header().Get("Allow") != "GET, PUT" {
		t.Fatalf("got %d Allow=%q, want 405 and %q", w.Code, w.Header().Get("Allow"), "GET, PUT")
	}

	r = httptest.NewRequest("GET", "http://127.0.0.1:8788/v1/status", nil)
	r.RemoteAddr = "192.0.2.7:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"loopback_required"`) {
		t.Fatalf("got %d %s, want 403 loopback_required", w.Code, w.Body.String())
	}
}
