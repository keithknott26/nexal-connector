package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runtimeClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "t_0123456789abcdef", true)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

func TestRuntimePathValidatesIdentity(t *testing.T) {
	if p, err := runtimePath("host-1", ""); err != nil || p != "/api/hosts/host-1/inference-runtime" {
		t.Errorf("open path = %q, %v", p, err)
	}
	if p, err := runtimePath("host-1", "s_2"); err != nil || p != "/api/hosts/host-1/inference-runtime/s_2" {
		t.Errorf("session path = %q, %v", p, err)
	}
	for _, bad := range [][2]string{{"", ""}, {"../x", ""}, {"host", "a/b"}, {"host/x", "s"}} {
		if _, err := runtimePath(bad[0], bad[1]); err == nil {
			t.Errorf("runtimePath(%q, %q) accepted", bad[0], bad[1])
		}
	}
}

func TestDownloadRuntimeRejectsBadArgumentsBeforeNetwork(t *testing.T) {
	hit := false
	c := runtimeClient(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	var buf bytes.Buffer
	for name, call := range map[string]func() error{
		"bad host":     func() error { return c.DownloadRuntime(context.Background(), "a/b", "s", 0, &buf, 1) },
		"bad session":  func() error { return c.DownloadRuntime(context.Background(), "h", "", 0, &buf, 1) },
		"negative idx": func() error { return c.DownloadRuntime(context.Background(), "h", "s", -1, &buf, 1) },
		"idx too big":  func() error { return c.DownloadRuntime(context.Background(), "h", "s", 128, &buf, 1) },
		"zero size":    func() error { return c.DownloadRuntime(context.Background(), "h", "s", 0, &buf, 0) },
		"huge size":    func() error { return c.DownloadRuntime(context.Background(), "h", "s", 0, &buf, 16<<30+1) },
	} {
		if call() == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if hit {
		t.Error("an invalid request reached the network")
	}
}

func TestDownloadRuntimeStreamsExactlyTheDeclaredSize(t *testing.T) {
	var path, auth string
	c := runtimeClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte("shard-bytes"))
	})
	var buf bytes.Buffer
	if err := c.DownloadRuntime(context.Background(), "h1", "s1", 3, &buf, 11); err != nil {
		t.Fatalf("download: %v", err)
	}
	if buf.String() != "shard-bytes" {
		t.Errorf("body = %q", buf.String())
	}
	if path != "/api/hosts/h1/inference-runtime/s1/files/3" {
		t.Errorf("path = %q", path)
	}
	if auth != "Bearer t_0123456789abcdef" {
		t.Errorf("authorization = %q", auth)
	}
}

func TestDownloadRuntimeRefusesLengthMismatchAndHTTPErrors(t *testing.T) {
	c := runtimeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files/9") {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("12345"))
	})
	var buf bytes.Buffer
	if err := c.DownloadRuntime(context.Background(), "h", "s", 0, &buf, 4); err == nil {
		t.Error("longer body than declared was accepted")
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes despite a length mismatch", buf.Len())
	}
	err := c.DownloadRuntime(context.Background(), "h", "s", 9, &buf, 5)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusNotFound {
		t.Errorf("want StatusError 404, got %v", err)
	}
}

func TestRuntimeLifecycleUsesExpectedRoutes(t *testing.T) {
	var got []string
	c := runtimeClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "DELETE" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"schemaVersion":1}`))
	})
	ctx := context.Background()
	if _, err := c.RenewRuntime(ctx, "h", "s"); err != nil {
		t.Errorf("renew: %v", err)
	}
	if err := c.CloseRuntime(ctx, "h", "s"); err != nil {
		t.Errorf("close: %v", err)
	}
	if _, err := c.OpenRuntime(ctx, "bad/host"); err == nil {
		t.Error("open accepted an invalid host")
	}
	want := []string{"POST /api/hosts/h/inference-runtime/s", "DELETE /api/hosts/h/inference-runtime/s"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("requests = %v, want %v", got, want)
	}
}
