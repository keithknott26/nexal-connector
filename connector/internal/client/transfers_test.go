package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTransferRoutes(t *testing.T) {
	var got []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/hosts/recv1/bundle-transfers":
			_, _ = w.Write([]byte(`{"id":"tr1","donorHostId":"donor1","receiverHostId":"recv1","state":"pending"}`))
		case r.Method == "GET":
			_, _ = w.Write([]byte(`{"id":"tr1","state":"ready","ciphertext":"abc"}`))
		case r.Method == "PUT":
			_, _ = w.Write([]byte(`{"accepted":true}`))
		default:
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tr, err := c.CreateTransfer(ctx, "recv1", "donor1", "pub")
	if err != nil || tr.ID != "tr1" || tr.DonorHostID != "donor1" {
		t.Fatalf("create: %+v %v", tr, err)
	}
	var create map[string]string
	if err := json.Unmarshal([]byte(bodies[0]), &create); err != nil || create["donorHostId"] != "donor1" || create["publicKey"] != "pub" {
		t.Fatalf("create body: %q", bodies[0])
	}
	tr, err = c.GetTransfer(ctx, "recv1", "tr1")
	if err != nil || tr.Ciphertext == nil || *tr.Ciphertext != "abc" {
		t.Fatalf("get: %+v %v", tr, err)
	}
	if err := c.PutTransfer(ctx, "recv1", "tr1", "ct"); err != nil {
		t.Fatal(err)
	}
	if err := c.AckTransfer(ctx, "recv1", "tr1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /api/hosts/recv1/bundle-transfers",
		"GET /api/hosts/recv1/bundle-transfers/tr1",
		"PUT /api/hosts/recv1/bundle-transfers/tr1",
		"POST /api/hosts/recv1/bundle-transfers/tr1/ack",
	}
	if len(got) != len(want) {
		t.Fatalf("routes %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route %d = %q want %q", i, got[i], want[i])
		}
	}
}

func TestTransferRejectsUnacceptedAndInvalidIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":false,"acknowledged":false}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.PutTransfer(ctx, "recv1", "tr1", "ct"); err == nil {
		t.Fatal("expected not-accepted error")
	}
	if err := c.AckTransfer(ctx, "recv1", "tr1"); err == nil {
		t.Fatal("expected not-acknowledged error")
	}
	if _, err := c.CreateTransfer(ctx, "recv1", "../bad", "pub"); err == nil {
		t.Fatal("expected invalid donor error")
	}
	if _, err := c.GetTransfer(ctx, "bad/host", "tr1"); err == nil {
		t.Fatal("expected invalid host error")
	}
	if err := c.PutTransfer(ctx, "recv1", "../x", "ct"); err == nil {
		t.Fatal("expected invalid id error")
	}
	if err := c.AckTransfer(ctx, "recv1", "a b"); err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestTransferPathBuildsAndRejectsIdentities(t *testing.T) {
	cases := []struct {
		host, id, want string
		ok             bool
	}{
		{"recv1", "", "/api/hosts/recv1/bundle-transfers", true},
		{"recv1", "tr1", "/api/hosts/recv1/bundle-transfers/tr1", true},
		{"", "", "", false},
		{"recv1", "../x", "", false},
		{"re/cv", "tr1", "", false},
		{"recv1", "tr 1", "", false},
	}
	for _, c := range cases {
		got, err := transferPath(c.host, c.id)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("transferPath(%q, %q) = %q, %v; want %q ok=%v", c.host, c.id, got, err, c.want, c.ok)
		}
	}
}
