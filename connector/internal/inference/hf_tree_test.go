package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTreeClient(t *testing.T, h http.HandlerFunc) *HFClient {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	c, err := NewHFClient(HFOptions{BaseURL: srv.URL, HTTPClient: srv.Client(), AllowHost: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestTreeListsEntriesAtRevision checks the request path and decoded entries.
func TestTreeListsEntriesAtRevision(t *testing.T) {
	c := newTreeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models/org/model/tree/abc123" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"type":"file","path":"config.json","size":12,"oid":"o1"},{"type":"file","path":"m.gguf","size":99,"oid":"o2","lfs":{"oid":"sha","size":99}}]`))
	})
	got, err := c.Tree(context.Background(), "org/model", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "config.json" || got[1].LFS == nil || got[1].LFS.OID != "sha" {
		t.Fatalf("unexpected entries: %+v", got)
	}
}

// TestTreeRefusesPaginatedListing ensures a truncated listing is never pinned.
func TestTreeRefusesPaginatedListing(t *testing.T) {
	c := newTreeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Link", `<https://example.test/next>; rel="next"`)
		_, _ = w.Write([]byte(`[]`))
	})
	_, err := c.Tree(context.Background(), "org/model", "abc123")
	wantCode(t, err, "tree_paginated")
}

// TestTreeRejectsBadRepo checks that an invalid repo fails before any request.
func TestTreeRejectsBadRepo(t *testing.T) {
	c := newTreeClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request made for invalid repo")
	})
	if _, err := c.Tree(context.Background(), "../etc", "abc123"); err == nil {
		t.Fatal("expected error")
	}
}
