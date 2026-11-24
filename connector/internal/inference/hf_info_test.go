package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestInfoParsesGatingAndLicenseShapes pins how Info normalises the several
// shapes the Hub API uses for "gated" and "license", and that a bad repo name
// is refused before any request is made.
func TestInfoParsesGatingAndLicenseShapes(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantGated   bool
		wantLicense string
	}{
		{"open with string license", `{"sha":"s1","gated":false,"cardData":{"license":"apache-2.0"}}`, false, "apache-2.0"},
		{"gated string", `{"sha":"s1","gated":"manual"}`, true, ""},
		{"private counts as gated", `{"sha":"s1","private":true}`, true, ""},
		{"gated null", `{"sha":"s1","gated":null}`, false, ""},
		{"single-element license list", `{"sha":"s1","cardData":{"license":["mit"]}}`, false, "mit"},
		{"multi-element license list falls back to tag", `{"sha":"s1","cardData":{"license":["mit","bsd"]},"tags":["x","license:gpl-3.0"]}`, false, "gpl-3.0"},
		{"license from tag", `{"sha":"s1","tags":["license:cc-by-4.0"]}`, false, "cc-by-4.0"},
		{"empty license tag ignored", `{"sha":"s1","tags":["license:"]}`, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/models/owner/name/revision/main" {
					t.Errorf("path = %q", r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := NewHFClient(HFOptions{BaseURL: srv.URL, HTTPClient: srv.Client(), AllowHost: func(string) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			info, err := c.Info(context.Background(), "owner/name", "main")
			if err != nil {
				t.Fatal(err)
			}
			if info.SHA != "s1" || info.Gated != tc.wantGated || info.License != tc.wantLicense {
				t.Fatalf("Info = %+v, want gated=%v license=%q", info, tc.wantGated, tc.wantLicense)
			}
		})
	}
}

func TestInfoRejectsInvalidRepoWithoutRequest(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	c, err := NewHFClient(HFOptions{BaseURL: srv.URL, HTTPClient: srv.Client(), AllowHost: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Info(context.Background(), "not a repo", "main")
	wantCode(t, err, "invalid_arguments")
	if hit {
		t.Fatal("request sent for an invalid repo name")
	}
}
