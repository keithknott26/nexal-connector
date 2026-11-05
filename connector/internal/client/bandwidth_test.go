package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestBandwidthOpenNamesThePeerByTunnelAddress(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v2/hosts/bandwidth-open" || r.Header.Get("Authorization") != "Bearer host-scoped-secret" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"requestId":"bw_1","delivered":true}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	if err := c.RequestBandwidthOpen(context.Background(), "100.86.56.136"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["tunnelAddress"] != "100.86.56.136" {
		t.Fatalf("body = %v", got)
	}
}

func TestRequestBandwidthOpenRefusesAnAddressOutsideTheMesh(t *testing.T) {
	c, _ := New("https://coordinator.invalid", "t", true)
	for _, bad := range []string{"", "8.8.8.8", "mac-mini-m4", "6LWVHzV0tr7kAuVU6oYHjWoBEFn6wEAoQ0Oyx+OBA2s="} {
		if err := c.RequestBandwidthOpen(context.Background(), bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestRequestBandwidthOpenIDReturnsOnlyAValidRequestID(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"valid", `{"requestId":"bw_1","delivered":true}`, "bw_1"},
		{"missing", `{"delivered":true}`, ""},
		{"invalid", `{"requestId":"bad id/../x","delivered":true}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(202)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "t", true)
			got, err := c.RequestBandwidthOpenID(context.Background(), "100.86.56.136")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("id = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequestBandwidthOpenIDPropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "t", true)
	id, err := c.RequestBandwidthOpenID(context.Background(), "100.86.56.136")
	if err == nil || id != "" {
		t.Fatalf("id = %q, err = %v; want an error and no id", id, err)
	}
}
