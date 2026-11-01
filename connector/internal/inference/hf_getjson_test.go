package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetJSONMapsStatusesAndBadBodies pins getJSON's error contract: each
// non-success response and each malformed body maps to a stable error code.
func TestGetJSONMapsStatusesAndBadBodies(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"not found", http.StatusNotFound, "", "repo_not_found"},
		{"unauthorized", http.StatusUnauthorized, "", "repo_not_public"},
		{"forbidden", http.StatusForbidden, "", "repo_not_public"},
		{"server error", http.StatusInternalServerError, "", "http_status"},
		{"too large", http.StatusOK, strings.Repeat("x", maxAPIBytes+1), "api_too_large"},
		{"not json", http.StatusOK, "<html>", "api_invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := NewHFClient(HFOptions{BaseURL: srv.URL, HTTPClient: srv.Client(), AllowHost: func(string) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			_, err = c.getJSON(context.Background(), c.base, &v)
			wantCode(t, err, tc.code)
		})
	}
}

func TestGetJSONDecodesAndReturnsHeaders(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("X-Test", "yes")
		_, _ = w.Write([]byte(`{"sha":"abc"}`))
	}))
	defer srv.Close()
	c, err := NewHFClient(HFOptions{BaseURL: srv.URL, HTTPClient: srv.Client(), AllowHost: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ SHA string }
	h, err := c.getJSON(context.Background(), c.base, &v)
	if err != nil || v.SHA != "abc" || h.Get("X-Test") != "yes" {
		t.Fatalf("got %+v, %v, %v", v, h, err)
	}
}
