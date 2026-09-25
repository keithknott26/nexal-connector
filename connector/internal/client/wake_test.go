package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/wsclient"
)

const wakeTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestReportWakeInfo(t *testing.T) {
	var got map[string]any
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v2/hosts/wake-info" || r.Header.Get("Authorization") != "Bearer host-scoped-secret" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		// Unknown fields are tolerated on this new endpoint (HARDENING-PLAN §43).
		_, _ = w.Write([]byte(`{"ok":true,"someFutureField":1}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	ctx := context.Background()
	err := c.ReportWakeInfo(ctx, WakeInfo{MACs: []string{"3C-22-FB-01-02-03"}, LANKey: wakeTestKey, WakeForNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	if macs, _ := got["macs"].([]any); len(macs) != 1 || macs[0] != "3c:22:fb:01:02:03" || got["lanKey"] != wakeTestKey || got["wakeForNetwork"] != true {
		t.Fatalf("body = %v", got)
	}
	for _, bad := range []WakeInfo{
		{MACs: nil, LANKey: wakeTestKey},
		{MACs: []string{"ff:ff:ff:ff:ff:ff"}, LANKey: wakeTestKey},
		{MACs: []string{"3c:22:fb:01:02:03"}, LANKey: "short"},
		{MACs: []string{"3c:22:fb:01:02:03"}, LANKey: strings.ToUpper(wakeTestKey)},
	} {
		if c.ReportWakeInfo(ctx, bad) == nil {
			t.Errorf("accepted invalid wake info %+v", bad)
		}
	}
	for _, s := range []int{404, 503} {
		status = s
		if err := c.ReportWakeInfo(ctx, WakeInfo{MACs: []string{"3c:22:fb:01:02:03"}, LANKey: wakeTestKey}); !IsNotSupported(err) {
			t.Fatalf("HTTP %d: IsNotSupported(%v) = false", s, err)
		}
	}
	status = 500
	if err := c.ReportWakeInfo(ctx, WakeInfo{MACs: []string{"3c:22:fb:01:02:03"}, LANKey: wakeTestKey}); err == nil || IsNotSupported(err) {
		t.Fatalf("HTTP 500 = %v; want a real failure", err)
	}
}

func TestRequestWake(t *testing.T) {
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method %s", r.Method)
		}
		switch r.URL.Path {
		case "/api/v2/hosts/ok-host/wake":
			reply(w, 200, `{"requested":true,"requestId":"req_1","relays":2,"extra":"ignored"}`)
		case "/api/v2/hosts/lonely/wake":
			reply(w, 409, `{"error":{"code":"no_wake_relay","message":"<script>untrusted prose</script>"}}`)
		case "/api/v2/hosts/other-conflict/wake":
			reply(w, 409, `{"error":{"code":"host_awake","message":"x"}}`)
		case "/api/v2/hosts/weird-code/wake":
			reply(w, 409, `{"error":{"code":"No Wake Relay\u001b[31m","message":"x"}}`)
		case "/api/v2/hosts/lying/wake":
			reply(w, 200, `{"requested":false,"requestId":"req_2","relays":1}`)
		default:
			reply(w, 404, `{}`)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	ctx := context.Background()
	ack, err := c.RequestWake(ctx, "ok-host")
	if err != nil || !ack.Requested || ack.RequestID != "req_1" || ack.Relays != 2 {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	if _, err := c.RequestWake(ctx, "lonely"); !errors.Is(err, ErrNoWakeRelay) {
		t.Fatalf("409 no_wake_relay = %v", err)
	}
	var s *StatusError
	_, err = c.RequestWake(ctx, "other-conflict")
	if !errors.As(err, &s) || s.Status != 409 || s.Code != "host_awake" || errors.Is(err, ErrNoWakeRelay) {
		t.Fatalf("other 409 = %v", err)
	}
	if strings.Contains(err.Error(), "host_awake") {
		// Error() is unchanged by the Code field.
		t.Fatal("StatusError.Error() now prints the code")
	}
	if _, err := c.RequestWake(ctx, "weird-code"); !errors.As(err, &s) || s.Code != "" {
		t.Fatalf("unsanitary code kept: %+v", s)
	}
	if _, err := c.RequestWake(ctx, "lying"); err == nil {
		t.Fatal("requested:false accepted")
	}
	if _, err := c.RequestWake(ctx, "missing"); !IsNotSupported(err) {
		t.Fatalf("404 = %v", err)
	}
	if _, err := c.RequestWake(ctx, "../etc"); err == nil {
		t.Fatal("invalid host id accepted")
	}
}

func TestDialHostEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/hosts/events" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer host-scoped-secret" {
			w.WriteHeader(401)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			wsclient.AcceptKey(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n")
		msg := `{"v":1,"type":"snapshot","online":[],"at":"2026-09-24T00:00:00Z"}`
		_, _ = rw.Write(append([]byte{0x81, byte(len(msg))}, msg...))
		_ = rw.Flush()
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	conn, err := c.DialHostEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, msg, err := conn.ReadMessage(); err != nil || !strings.Contains(string(msg), "snapshot") {
		t.Fatalf("%q %v", msg, err)
	}
	_ = conn.Close()

	wrong, _ := New(srv.URL, "wrong-token-value", true)
	_, err = wrong.DialHostEvents(context.Background())
	var s *StatusError
	if !errors.As(err, &s) || s.Status != 401 || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("401 upgrade = %v", err)
	}
	// Production (dev=false) must refuse plain http even for loopback — the same
	// rule as every REST call — and must not leak the URL.
	if _, err := New(srv.URL, "host-scoped-secret", false); err == nil {
		t.Fatal("production client accepted an http origin")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	down, _ := New(url, "host-scoped-secret", true)
	if _, err := down.DialHostEvents(context.Background()); err == nil || strings.Contains(err.Error(), url) ||
		!strings.Contains(err.Error(), "coordinator request failed") {
		t.Fatalf("transport error not sanitized: %v", err)
	}
}
