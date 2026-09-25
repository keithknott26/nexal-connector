package client

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const testLANKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestPutWakeInfoContract(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v2/hosts/wake-info" || r.Header.Get("Authorization") != "Bearer host-token-123456" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		ioJSON(w, map[string]any{"ok": true, "macCount": len(got["macs"].([]any)), "wakeForNetwork": got["wakeForNetwork"]})
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-token-123456", true)
	ctx := context.Background()
	if err := c.PutWakeInfo(ctx, WakeInfo{MACs: []string{"a4:83:e7:12:34:56", "3c:22:fb:00:00:01"}, LANKey: testLANKey, WakeForNetwork: true, TunnelAddress: "100.113.174.101"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"macs": []any{"a4:83:e7:12:34:56", "3c:22:fb:00:00:01"}, "lanKey": testLANKey, "wakeForNetwork": true, "tunnelAddress": "100.113.174.101"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if err := c.PutWakeInfo(ctx, WakeInfo{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: testLANKey}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["tunnelAddress"]; ok || len(got) != 3 {
		t.Fatalf("unknown tunnel address must be omitted: %v", got)
	}
}

func TestWakeInfoValidation(t *testing.T) {
	ok := WakeInfo{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: testLANKey}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = "a4:83:e7:12:34:5" + string(rune('0'+i))
	}
	bad := []WakeInfo{
		{LANKey: testLANKey},
		{MACs: nine, LANKey: testLANKey},
		{MACs: []string{"A4:83:E7:12:34:56"}, LANKey: testLANKey},
		{MACs: []string{"a4-83-e7-12-34-56"}, LANKey: testLANKey},
		{MACs: []string{"01:00:5e:00:00:01"}, LANKey: testLANKey}, // multicast
		{MACs: []string{"00:00:00:00:00:00"}, LANKey: testLANKey},
		{MACs: []string{"a4:83:e7:12:34:56", "a4:83:e7:12:34:56"}, LANKey: testLANKey},
		{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: strings.ToUpper(testLANKey)},
		{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: testLANKey[:63]},
		{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: testLANKey, TunnelAddress: "192.168.1.2"},
		{MACs: []string{"a4:83:e7:12:34:56"}, LANKey: testLANKey, TunnelAddress: "100.128.0.1"},
	}
	c, _ := New("http://127.0.0.1:1", "host-token-123456", true)
	for i, w := range bad {
		if w.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
		if c.PutWakeInfo(context.Background(), w) == nil {
			t.Errorf("case %d sent", i)
		}
	}
}

func TestWakeRequestsContract(t *testing.T) {
	var method, path, body string
	status := 202
	resp := `{"requestId":"6f1c2a52-8a4b-4d5e-9c1f-0a1b2c3d4e5f","relays":2,"targetWakeForNetwork":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.Path, string(b)
		if r.Header.Get("Authorization") != "Bearer host-token-123456" {
			t.Error("missing host auth")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "host-token-123456", true)
	ctx := context.Background()
	res, err := c.WakeByTunnel(ctx, "100.113.99.69")
	if err != nil || res.RequestID != "6f1c2a52-8a4b-4d5e-9c1f-0a1b2c3d4e5f" || res.Relays != 2 || !res.TargetWakeForNetwork {
		t.Fatalf("%+v %v", res, err)
	}
	if method != "POST" || path != "/api/v2/hosts/wake" || body != `{"tunnelAddress":"100.113.99.69"}` {
		t.Fatalf("%s %s %s", method, path, body)
	}
	if _, err = c.WakeHost(ctx, "host_abc"); err != nil || path != "/api/v2/hosts/host_abc/wake" || body != "" {
		t.Fatalf("%s %q %v", path, body, err)
	}
	for _, code := range []int{400, 404, 409, 429, 503} {
		status = code
		var se *StatusError
		if _, err = c.WakeHost(ctx, "host_abc"); !errors.As(err, &se) || se.Status != code {
			t.Fatalf("status %d: %v", code, err)
		}
	}
	status = 202
	resp = `{"requestId":"r1","relays":0,"targetWakeForNetwork":false,"extra":1}`
	if _, err = c.WakeHost(ctx, "host_abc"); err == nil {
		t.Fatal("unknown response field accepted")
	}
	resp = `{"requestId":"","relays":0,"targetWakeForNetwork":false}`
	if _, err = c.WakeHost(ctx, "host_abc"); err == nil {
		t.Fatal("empty request id accepted")
	}
	if _, err = c.WakeByTunnel(ctx, "10.0.0.1"); err == nil {
		t.Fatal("non-tunnel address accepted")
	}
	if _, err = c.WakeHost(ctx, "../x"); err == nil {
		t.Fatal("invalid host id accepted")
	}
}

// upgrade is a minimal server-side WebSocket accept that then sends one text
// frame.
func upgrade(t *testing.T, w http.ResponseWriter, r *http.Request, text string) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	_, _ = rw.Write(append([]byte{0x81, byte(len(text))}, text...))
	_ = rw.Flush()
	_, _ = rw.ReadByte() // wait for the client's close
}

func TestDialEvents(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EventsPath {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer host-token-123456" {
			w.WriteHeader(401)
			return
		}
		upgrade(t, w, r, `{"v":1,"type":"snapshot","online":[],"at":"x"}`)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c, _ := New(srv.URL, "host-token-123456", true)
	conn, err := c.DialEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, msg, err := conn.ReadMessage(); err != nil || !strings.Contains(string(msg), "snapshot") {
		t.Fatalf("%q %v", msg, err)
	}
	conn.Close(1000)

	bad, _ := New(srv.URL, "wrong-token-123456", true)
	var se *StatusError
	if _, err := bad.DialEvents(context.Background()); !errors.As(err, &se) || se.Status != 401 {
		t.Fatalf("err %v", err)
	}

	// wss:// with the REST transport's TLS configuration (TLS 1.3 floor).
	tlsSrv := httptest.NewTLSServer(handler)
	defer tlsSrv.Close()
	sc, err := New(tlsSrv.URL, "host-token-123456", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.DialEvents(context.Background()); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	cfg := sc.http.Transport.(*http.Transport).TLSClientConfig
	cfg.RootCAs = tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	var version uint16
	cfg.VerifyConnection = func(cs tls.ConnectionState) error { version = cs.Version; return nil }
	conn, err = sc.DialEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(1000)
	if version != tls.VersionTLS13 {
		t.Fatalf("TLS version %#x", version)
	}
	if _, msg, err := conn.ReadMessage(); err != nil || !strings.Contains(string(msg), "snapshot") {
		t.Fatalf("%q %v", msg, err)
	}
}
