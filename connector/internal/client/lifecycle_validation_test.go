package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostAndAttemptCallsRejectBadIDsBeforeAnyRequest(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	c, err := New(server.URL, "host-scoped-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Heartbeat(ctx, "../x", Heartbeat{}); err == nil {
		t.Error("Heartbeat accepted invalid host id")
	}
	if _, err := c.Renew(ctx, "a/b"); err == nil {
		t.Error("Renew accepted invalid attempt id")
	}
	if _, err := c.Complete(ctx, "", Result{}, 1); err == nil {
		t.Error("Complete accepted invalid attempt id")
	}
	if hits != 0 {
		t.Fatalf("invalid ids reached the network %d times", hits)
	}
}

func TestHeartbeatAndRenewRejectNonOKReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer server.Close()
	c, err := New(server.URL, "host-scoped-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Heartbeat(context.Background(), "h1", Heartbeat{}); err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Errorf("Heartbeat err = %v", err)
	}
	if _, err := c.Renew(context.Background(), "a1"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("Renew err = %v", err)
	}
}

func TestEnrollRejectsMalformedIdentity(t *testing.T) {
	good := strings.Repeat("t", 32)
	for name, body := range map[string]string{
		"bad host id":      `{"hostId":"../x","token":"` + good + `"}`,
		"short token":      `{"hostId":"h1","token":"short"}`,
		"whitespace token": `{"hostId":"h1","token":"` + strings.Repeat("t", 15) + ` ` + strings.Repeat("t", 16) + `"}`,
		"oversize token":   `{"hostId":"h1","token":"` + strings.Repeat("t", 4097) + `"}`,
	} {
		body := body
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		c, err := New(server.URL, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Enroll(context.Background(), Enrollment{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
		server.Close()
	}
}
