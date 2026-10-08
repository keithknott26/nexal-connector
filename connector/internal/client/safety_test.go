package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"nexal/connector/internal/observability"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRejectAmbiguousCoordinatorResponses(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate_flag": `{"ok":false,"ok":true}`,
		"case_alias":     `{"ok":false,"OK":true}`,
		"escaped_alias":  `{"ok":false,"\u006fk":true}`,
		"null_root":      `null`,
		"array_root":     `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "test-host-credential", true)
			if _, err := c.Heartbeat(context.Background(), "h1", Heartbeat{}); err == nil {
				t.Fatal("ambiguous coordinator acknowledgement accepted")
			}
		})
	}
}

func TestCoordinatorBodyAndHeadersRespectCancellation(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "body" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "test-host-credential", true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.Next(ctx, "h1"); done <- err }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request not received")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil || strings.Contains(err.Error(), srv.URL) ||
					strings.Contains(err.Error(), "test-host-credential") {
					t.Fatal("cancellation accepted or exposed request details")
				}
			case <-time.After(time.Second):
				t.Fatal("request ignored cancellation")
			}
		})
	}
}

func TestEnrollmentNeverFollowsAnyRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var hits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				hits.Add(1)
			}))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/secret-in-location", status)
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "", true)
			_, err := c.Enroll(context.Background(), Enrollment{Code: "one-use-enrollment-secret"})
			if err == nil || hits.Load() != 0 || strings.Contains(err.Error(), "secret") ||
				strings.Contains(err.Error(), srv.URL) {
				t.Fatal("redirect followed or sensitive details leaked")
			}
		})
	}
}

func TestClientRejectsMalformedHostCredentials(t *testing.T) {
	for _, token := range []string{"has space", "has\ttab", "has\x00nul", "has\x7fdel", "has\u00e9unicode", strings.Repeat("x", 4097)} {
		if _, err := New("https://coordinator.example", token, false); err == nil {
			t.Fatal("unsafe header credential accepted")
		}
	}
	for _, token := range []string{"", "host-scoped-secret"} {
		if _, err := New("https://coordinator.example", token, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientSafetyDefaults(t *testing.T) {
	c, err := New("https://coordinator.example", "", false)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := observability.Underlying(c.http.Transport).(*http.Transport)
	if !ok || tr.Proxy != nil || tr.TLSClientConfig.InsecureSkipVerify ||
		tr.TLSHandshakeTimeout <= 0 || tr.ResponseHeaderTimeout <= 0 ||
		c.http.Timeout <= 0 || c.http.Timeout > 10*time.Second {
		t.Fatal("unbounded or ambient-credential-forwarding transport")
	}
}
