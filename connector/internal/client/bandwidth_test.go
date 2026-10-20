package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestBandwidthOpenPostsToPeer(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"requestId":"r1","delivered":true}`))
	}))
	defer server.Close()
	c, err := New(server.URL, "host-credential-value", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RequestBandwidthOpen(context.Background(), "peer-one"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v2/hosts/peer-one/bandwidth-open" {
		t.Errorf("request %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer host-credential-value" {
		t.Error("missing host bearer")
	}
}

func TestRequestBandwidthOpenRejectsBadHostIDBeforeNetwork(t *testing.T) {
	c := &Client{}
	for _, id := range []string{"", "a/b", "../x", "has space"} {
		if err := c.RequestBandwidthOpen(context.Background(), id); err == nil {
			t.Errorf("expected invalid host id error for %q", id)
		}
	}
}
