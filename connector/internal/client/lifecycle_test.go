package client

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCancelEnrollmentAndLeaveMeshRequireExplicitConfirmation(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174001"
	confirm := true
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v2/enrollment-sessions/" + id + "/cancel":
			ioJSON(w, map[string]any{"cancelled": confirm})
		case "/api/v2/mesh/leave":
			ioJSON(w, map[string]any{"left": confirm})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, "host-credential-value", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.CancelEnrollmentSession(ctx, id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := c.LeaveMeshNetwork(ctx); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if len(paths) != 2 || paths[0] != "POST /api/v2/enrollment-sessions/"+id+"/cancel" || paths[1] != "POST /api/v2/mesh/leave" {
		t.Fatalf("requests %v", paths)
	}

	confirm = false
	if err := c.CancelEnrollmentSession(ctx, id); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("unconfirmed cancel accepted: %v", err)
	}
	if err := c.LeaveMeshNetwork(ctx); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("unconfirmed leave accepted: %v", err)
	}

	before := len(paths)
	if err := c.CancelEnrollmentSession(ctx, "../etc"); err == nil {
		t.Fatal("non-UUID session id accepted")
	}
	if len(paths) != before {
		t.Fatal("invalid id reached the network")
	}
}

func TestRequestLoggerRecordsPathWithoutQuery(t *testing.T) {
	var buf bytes.Buffer
	c := &Client{}
	c.logRequest("GET", "/x", 200, time.Now(), nil) // nil logger must be a no-op
	c.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	c.logRequest("GET", "/api/v2/thing?secret=abc", 502, time.Now(), context.DeadlineExceeded)
	out := buf.String()
	for _, want := range []string{"path=/api/v2/thing", "status=502", "method=GET", "error="} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q missing %q", out, want)
		}
	}
	if strings.Contains(out, "secret") {
		t.Errorf("query string leaked into log: %q", out)
	}
}
