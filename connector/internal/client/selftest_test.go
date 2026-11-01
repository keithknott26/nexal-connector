package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSelfTestPollsTheHostRouteAndDecodesPendingAttempt(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		ioJSON(w, map[string]any{"pending": true, "attempt": Attempt{ID: "a9", HostID: "h1"}})
	}))
	defer srv.Close()
	c, err := New(srv.URL, "secret", true)
	if err != nil {
		t.Fatal(err)
	}
	pending, at, err := c.SelfTest(context.Background(), "h1")
	if err != nil || !pending || at == nil || at.ID != "a9" {
		t.Fatalf("SelfTest = %v, %+v, %v", pending, at, err)
	}
	if method != "GET" || path != "/api/hosts/h1/self-test" {
		t.Fatalf("request %s %s", method, path)
	}
}

func TestSelfTestReportsNothingPendingAndRejectsBadHostOrServerError(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"pending":false}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "secret", true)
	if err != nil {
		t.Fatal(err)
	}
	if pending, at, err := c.SelfTest(context.Background(), "h1"); err != nil || pending || at != nil {
		t.Fatalf("idle SelfTest = %v, %+v, %v", pending, at, err)
	}
	if _, _, err := c.SelfTest(context.Background(), "../admin"); err == nil {
		t.Fatal("invalid host id accepted")
	}
	status = http.StatusInternalServerError
	if _, _, err := c.SelfTest(context.Background(), "h1"); err == nil {
		t.Fatal("server error not surfaced")
	}
}
