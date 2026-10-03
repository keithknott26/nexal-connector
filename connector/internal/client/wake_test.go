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
			// The coordinator's real answer: 202, no "requested" field.
			reply(w, 202, `{"requestId":"6f1c2a4e-9b1d-4c8e-8a57-3f0e2d1b9c77","relays":2,"targetWakeForNetwork":true,"extra":"ignored"}`)
		case "/api/v2/hosts/zero-relay/wake":
			reply(w, 200, `{"requestId":"req_0","relays":0,"targetWakeForNetwork":false}`)
		case "/api/v2/hosts/busy/wake":
			reply(w, 429, `{"error":{"code":"rate_limited","message":"x"}}`)
		case "/api/v2/hosts/gone/wake":
			reply(w, 404, `{"error":{"code":"wake_target_not_found","message":"x"}}`)
		case "/api/v2/hosts/gated/wake":
			reply(w, 503, `{"error":{"code":"wake_gate_unavailable","message":"x"}}`)
		case "/api/v2/hosts/too-many-relays/wake":
			reply(w, 202, `{"requestId":"req_3","relays":1001}`)
		case "/api/v2/hosts/no-id/wake":
			reply(w, 202, `{"relays":1}`)
		case "/api/v2/hosts/lonely/wake":
			reply(w, 409, `{"error":{"code":"no_wake_relay","message":"<script>untrusted prose</script>"}}`)
		case "/api/v2/hosts/other-conflict/wake":
			reply(w, 409, `{"error":{"code":"host_awake","message":"x"}}`)
		case "/api/v2/hosts/weird-code/wake":
			reply(w, 409, `{"error":{"code":"No Wake Relay\u001b[31m","message":"x"}}`)
		default:
			reply(w, 404, `{}`)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	ctx := context.Background()
	ack, err := c.RequestWake(ctx, "ok-host")
	if err != nil || ack.RequestID != "6f1c2a4e-9b1d-4c8e-8a57-3f0e2d1b9c77" || ack.Relays != 2 || !ack.TargetWakeForNetwork {
		t.Fatalf("202 ack = %+v, %v", ack, err)
	}
	if ack, err := c.RequestWake(ctx, "zero-relay"); err != nil || ack.Relays != 0 || ack.TargetWakeForNetwork {
		t.Fatalf("200 zero-relay ack = %+v, %v", ack, err)
	}
	if _, err := c.RequestWake(ctx, "busy"); !errors.Is(err, ErrWakeRateLimited) {
		t.Fatalf("429 = %v", err)
	}
	if _, err := c.RequestWake(ctx, "gone"); !errors.Is(err, ErrWakeTargetNotFound) || IsNotSupported(err) {
		t.Fatalf("404 wake_target_not_found = %v", err)
	}
	if _, err := c.RequestWake(ctx, "gated"); !IsNotSupported(err) {
		t.Fatalf("503 = %v", err)
	}
	for _, id := range []string{"too-many-relays", "no-id"} {
		if _, err := c.RequestWake(ctx, id); err == nil {
			t.Fatalf("%s accepted", id)
		}
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
	if _, err := c.RequestWake(ctx, "missing"); !IsNotSupported(err) {
		t.Fatalf("404 = %v", err)
	}
	if _, err := c.RequestWake(ctx, "../etc"); err == nil {
		t.Fatal("invalid host id accepted")
	}
}

// TestCallLenientAccepts2xx pins that the lenient path treats the whole 2xx
// range as success, including 202 Accepted (wake) and 204 No Content.
func TestCallLenientAccepts2xx(t *testing.T) {
	for _, status := range []int{200, 201, 202, 204} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			if status != 204 {
				_, _ = w.Write([]byte(`{"requestId":"r1","relays":1,"unknown":true}`))
			}
		}))
		c, _ := New(srv.URL, "host-scoped-secret", true)
		if err := c.callLenient(context.Background(), "PUT", "/x", struct{}{}, nil); err != nil {
			t.Errorf("HTTP %d with nil out: %v", status, err)
		}
		if status != 204 {
			var out WakeAck
			if err := c.callLenient(context.Background(), "POST", "/x", struct{}{}, &out); err != nil || out.RequestID != "r1" {
				t.Errorf("HTTP %d decode: %+v %v", status, out, err)
			}
		}
		srv.Close()
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

func TestRequestWakeByTunnel(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v2/hosts/wake" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body["tunnelAddress"] == "100.64.0.9" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"wake_target_not_found","message":"x"}}`))
			return
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"requestId":"6f1c2a4e-9b1d-4c8e-8a57-3f0e2d1b9c77","relays":1,"targetWakeForNetwork":false}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	ack, err := c.RequestWakeByTunnel(context.Background(), "100.113.99.69")
	if err != nil || ack.Relays != 1 || ack.TargetWakeForNetwork || body["tunnelAddress"] != "100.113.99.69" || len(body) != 1 {
		t.Fatalf("ack %+v err %v body %v", ack, err, body)
	}
	if _, err := c.RequestWakeByTunnel(context.Background(), "100.64.0.9"); !errors.Is(err, ErrWakeTargetNotFound) {
		t.Fatalf("want ErrWakeTargetNotFound, got %v", err)
	}
	for _, bad := range []string{"192.168.1.5", "100.128.0.1", "100.064.0.1", ""} {
		if _, err := c.RequestWakeByTunnel(context.Background(), bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestReportWakeInfoTunnelAddress(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"macCount":1,"wakeForNetwork":true}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-scoped-secret", true)
	key := strings.Repeat("a", 64)
	if err := c.ReportWakeInfo(context.Background(), WakeInfo{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: key, WakeForNetwork: true, TunnelAddress: "100.113.174.101"}); err != nil {
		t.Fatal(err)
	}
	if got["tunnelAddress"] != "100.113.174.101" {
		t.Fatalf("body %v", got)
	}
	if err := c.ReportWakeInfo(context.Background(), WakeInfo{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: key}); err != nil {
		t.Fatal(err)
	}
	if _, present := got["tunnelAddress"]; present {
		t.Fatalf("empty tunnelAddress must be omitted: %v", got)
	}
	if err := c.ReportWakeInfo(context.Background(), WakeInfo{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: key, TunnelAddress: "10.0.0.1"}); err == nil {
		t.Fatal("non-tunnel address accepted")
	}
}
