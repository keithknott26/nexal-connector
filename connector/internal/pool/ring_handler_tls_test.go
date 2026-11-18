package pool

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The inbox's first gate is transport: a request with no TLS state, or with
// anything below TLS 1.3, is refused as unauthorized before the policy, the
// method, the path or the body are looked at.
func TestRingHandlerRefusesNonTLS13BeforeAnythingElse(t *testing.T) {
	h := (&ringTransport{}).handler(&Ring{})
	for name, state := range map[string]*tls.ConnectionState{
		"no tls":  nil,
		"tls 1.2": {Version: tls.VersionTLS12},
	} {
		req := httptest.NewRequest(http.MethodGet, "/not-the-collective-path?x=1", strings.NewReader(""))
		req.TLS = state
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", name, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: security headers missing: %v", name, rec.Header())
		}
	}
}
