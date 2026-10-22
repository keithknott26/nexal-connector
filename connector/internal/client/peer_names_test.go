package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidPeerAddress(t *testing.T) {
	for in, want := range map[string]bool{
		"100.64.0.1":        true,
		"100.127.255.254":   true,
		"100.63.255.255":    false,
		"100.128.0.1":       false,
		"10.0.0.1":          false,
		"::ffff:100.64.0.1": false,
		"fd7a::1":           false,
		"":                  false,
		"not-an-ip":         false,
	} {
		if got := ValidPeerAddress(in); got != want {
			t.Errorf("ValidPeerAddress(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCleanPeerName(t *testing.T) {
	if got, err := CleanPeerName("  Studio Mac \n"); err != nil || got != "Studio Mac" {
		t.Errorf("trim: got %q, %v", got, err)
	}
	if got, err := CleanPeerName("   "); err != nil || got != "" {
		t.Errorf("blank removes the name: got %q, %v", got, err)
	}
	if _, err := CleanPeerName(strings.Repeat("é", MaxPeerName)); err != nil {
		t.Errorf("limit counts characters, not bytes: %v", err)
	}
	if _, err := CleanPeerName(strings.Repeat("a", MaxPeerName+1)); err == nil {
		t.Error("expected too-long error")
	}
	if _, err := CleanPeerName("bad\x00name"); err == nil {
		t.Error("expected control character error")
	}
}

func TestSetPeerNameRejectsBadInputBeforeNetwork(t *testing.T) {
	c := &Client{}
	if _, err := c.SetPeerName(nil, "host", "8.8.8.8", "x"); err == nil {
		t.Error("expected invalid address error")
	}
	if _, err := c.SetPeerName(nil, "host", "100.64.0.1", "bad\tname"); err == nil {
		t.Error("expected invalid name error")
	}
}

func TestPeerNamesRoutesAndBodies(t *testing.T) {
	var got, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"names":[{"address":"100.64.0.1","name":"Studio"}]}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	out, err := c.PeerNames(ctx, "h1")
	if err != nil || !strings.Contains(string(out), "Studio") {
		t.Fatalf("PeerNames: %s, %v", out, err)
	}
	if _, err := c.SetPeerName(ctx, "h1", "100.64.0.1", "  Studio "); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPeerName(ctx, "h1", "100.64.0.1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PeerNames(ctx, "bad id/../x"); err == nil {
		t.Error("expected invalid host id error")
	}
	wantRoutes := []string{
		"GET /api/v2/hosts/h1/peer-names",
		"PUT /api/v2/hosts/h1/peer-names/100.64.0.1",
		"PUT /api/v2/hosts/h1/peer-names/100.64.0.1",
	}
	if strings.Join(got, "|") != strings.Join(wantRoutes, "|") {
		t.Fatalf("routes = %v, want %v", got, wantRoutes)
	}
	if bodies[1] != `{"name":"Studio"}` {
		t.Errorf("set body = %s", bodies[1])
	}
	if bodies[2] != `{"name":null}` {
		t.Errorf("an empty name must send null to remove it, got %s", bodies[2])
	}
}
