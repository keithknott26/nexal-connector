package wsclient

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These tests run a REAL upgrade against httptest: the server side below is a
// minimal, test-only RFC 6455 peer written independently of the client code, so
// a bug that is symmetric in both halves cannot hide (the Accept proof is
// computed here from the RFC's GUID, not by calling AcceptKey).

const rfcGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type peer struct {
	conn net.Conn
	rw   *bufio.ReadWriter
	t    *testing.T
}

// serverFrame writes an UNMASKED server frame.
func (p *peer) frame(fin bool, op byte, payload []byte) {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	h := []byte{b0}
	switch n := len(payload); {
	case n <= 125:
		h = append(h, byte(n))
	case n <= 0xFFFF:
		h = append(h, 126, byte(n>>8), byte(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	_, _ = p.rw.Write(append(h, payload...))
	_ = p.rw.Flush()
}

// read reads one CLIENT frame, asserting it is masked, and returns it unmasked.
func (p *peer) read() (op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(p.rw, h[:]); err != nil {
		return 0, nil, err
	}
	if h[1]&0x80 == 0 {
		p.t.Error("client frame was not masked")
	}
	n := uint64(h[1] & 0x7F)
	if n == 126 {
		var e [2]byte
		_, _ = io.ReadFull(p.rw, e[:])
		n = uint64(binary.BigEndian.Uint16(e[:]))
	} else if n == 127 {
		var e [8]byte
		_, _ = io.ReadFull(p.rw, e[:])
		n = binary.BigEndian.Uint64(e[:])
	}
	var mask [4]byte
	if _, err = io.ReadFull(p.rw, mask[:]); err != nil {
		return 0, nil, err
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(p.rw, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return h[0] & 0x0F, payload, nil
}

type serverOpts struct {
	badAccept  bool
	status     int
	wantBearer string
}

func upgradeHandler(t *testing.T, o serverOpts, script func(*peer)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.wantBearer != "" && r.Header.Get("Authorization") != "Bearer "+o.wantBearer {
			w.WriteHeader(401)
			return
		}
		if o.status != 0 {
			w.WriteHeader(o.status)
			return
		}
		if r.Header.Get("Sec-WebSocket-Version") != "13" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Error("client did not send a websocket upgrade")
			w.WriteHeader(400)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		if key == "" {
			t.Error("missing key")
		}
		accept := acceptFor(key)
		if o.badAccept {
			accept = acceptFor("not-the-key")
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
		_ = rw.Flush()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		script(&peer{conn: conn, rw: rw, t: t})
	})
}

// acceptFor is computed here from the RFC's GUID, independently of AcceptKey.
func acceptFor(key string) string {
	h := sha1.Sum([]byte(key + rfcGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func TestAcceptKeyMatchesRFCExample(t *testing.T) {
	// The worked example in RFC 6455 §1.3.
	if AcceptKey("dGhlIHNhbXBsZSBub25jZQ==") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal("AcceptKey does not match the RFC 6455 §1.3 example")
	}
}

func dialPlain(t *testing.T, srv *httptest.Server, bearer string) (*Conn, error) {
	t.Helper()
	h := http.Header{}
	if bearer != "" {
		h.Set("Authorization", "Bearer "+bearer)
	}
	return Dial(context.Background(), Options{URL: srv.URL + "/api/v2/hosts/events", Header: h,
		AllowLoopbackHTTP: true, HandshakeTimeout: 3 * time.Second})
}

func TestRoundTripFragmentsPingAndClose(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(upgradeHandler(t, serverOpts{wantBearer: "tok"}, func(p *peer) {
		defer close(done)
		// Client "ping" text frame arrives masked.
		op, payload, err := p.read()
		if err != nil || op != opText || string(payload) != "ping" {
			t.Errorf("server got op=%d payload=%q err=%v", op, payload, err)
		}
		p.frame(true, opText, []byte(`{"v":1}`))
		// Fragmented message with a control ping in the middle (§5.4).
		p.frame(false, opText, []byte("hel"))
		p.frame(true, opPing, []byte("abc"))
		p.frame(false, opContinuation, []byte("lo "))
		p.frame(true, opContinuation, []byte("world"))
		// The client must answer the ping with a pong echoing the payload.
		op, payload, err = p.read()
		if err != nil || op != opPong || string(payload) != "abc" {
			t.Errorf("expected pong abc, got op=%d payload=%q err=%v", op, payload, err)
		}
		// A 16-bit length frame.
		p.frame(true, opBinary, make([]byte, 300))
		close4001 := []byte{0x0F, 0xA1}
		p.frame(true, opClose, append(close4001, []byte("removed")...))
		op, payload, err = p.read()
		if err != nil || op != opClose || len(payload) < 2 || binary.BigEndian.Uint16(payload) != 4001 {
			t.Errorf("client did not echo close 4001: op=%d payload=%v err=%v", op, payload, err)
		}
	}))
	defer srv.Close()
	c, err := dialPlain(t, srv, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.WriteText([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	typ, msg, err := c.ReadMessage()
	if err != nil || typ != TextMessage || string(msg) != `{"v":1}` {
		t.Fatalf("first message: %v %q %v", typ, msg, err)
	}
	typ, msg, err = c.ReadMessage()
	if err != nil || typ != TextMessage || string(msg) != "hello world" {
		t.Fatalf("reassembled message: %v %q %v", typ, msg, err)
	}
	typ, msg, err = c.ReadMessage()
	if err != nil || typ != BinaryMessage || len(msg) != 300 {
		t.Fatalf("binary message: %v %d %v", typ, len(msg), err)
	}
	_, _, err = c.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != 4001 || ce.Reason != "removed" {
		t.Fatalf("want CloseError 4001, got %v", err)
	}
	if err := c.WriteText([]byte("ping")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close = %v, want ErrClosed", err)
	}
	<-done
}

func TestHandshakeRejections(t *testing.T) {
	t.Run("bad accept", func(t *testing.T) {
		srv := httptest.NewServer(upgradeHandler(t, serverOpts{badAccept: true}, func(*peer) {}))
		defer srv.Close()
		if _, err := dialPlain(t, srv, ""); !errors.Is(err, ErrProtocol) {
			t.Fatalf("want ErrProtocol, got %v", err)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		srv := httptest.NewServer(upgradeHandler(t, serverOpts{wantBearer: "right"}, func(*peer) {}))
		defer srv.Close()
		_, err := dialPlain(t, srv, "wrong")
		var he *HandshakeError
		if !errors.As(err, &he) || he.Status != 401 {
			t.Fatalf("want HandshakeError 401, got %v", err)
		}
		if strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), srv.URL) {
			t.Fatal("handshake error leaked the token or URL")
		}
	})
	t.Run("not found is not followed or read", func(t *testing.T) {
		srv := httptest.NewServer(upgradeHandler(t, serverOpts{status: 404}, func(*peer) {}))
		defer srv.Close()
		var he *HandshakeError
		if _, err := dialPlain(t, srv, ""); !errors.As(err, &he) || he.Status != 404 {
			t.Fatalf("want 404, got %v", err)
		}
	})
	t.Run("redirect is refused", func(t *testing.T) {
		srv := httptest.NewServer(upgradeHandler(t, serverOpts{status: 307}, func(*peer) {}))
		defer srv.Close()
		var he *HandshakeError
		if _, err := dialPlain(t, srv, ""); !errors.As(err, &he) || he.Status != 307 {
			t.Fatalf("want 307 refusal, got %v", err)
		}
	})
	t.Run("negotiated extension", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, rw, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Extensions: permessage-deflate\r\nSec-WebSocket-Accept: " +
				AcceptKey(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n")
			_ = rw.Flush()
		}))
		defer srv.Close()
		if _, err := dialPlain(t, srv, ""); !errors.Is(err, ErrProtocol) {
			t.Fatalf("want ErrProtocol, got %v", err)
		}
	})
}

func TestOriginPolicy(t *testing.T) {
	cases := []Options{
		{URL: "http://127.0.0.1:1/x"},                                                  // http without the dev flag
		{URL: "http://example.com/x", AllowLoopbackHTTP: true},                         // http to a non-loopback host
		{URL: "http://localhost:1/x", AllowLoopbackHTTP: true},                         // names are not numeric loopback
		{URL: "ws://127.0.0.1:1/x", AllowLoopbackHTTP: true},                           // callers pass http(s) only
		{URL: "https://user:pw@example.com/x"},                                         // credentials in URL
		{URL: "https://example.com/x", Header: http.Header{"X": {"a\r\nInjected: 1"}}}, // header split
	}
	for _, o := range cases {
		o.HandshakeTimeout = time.Second
		if _, err := Dial(context.Background(), o); err == nil {
			t.Errorf("Dial(%q) succeeded; want refusal", o.URL)
		}
	}
}

func TestTLSUpgrade(t *testing.T) {
	srv := httptest.NewTLSServer(upgradeHandler(t, serverOpts{}, func(p *peer) {
		p.frame(true, opText, []byte("over tls"))
		_, _, _ = p.read() // wait for the client's close
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tlsCfg := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tlsCfg.RootCAs = pool
	c, err := Dial(context.Background(), Options{URL: srv.URL + "/events", TLSConfig: tlsCfg, HandshakeTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "over tls" {
		t.Fatalf("got %q %v", msg, err)
	}
}

func TestProtocolViolations(t *testing.T) {
	cases := map[string]struct {
		script func(*peer)
		want   error
	}{
		"oversized frame": {func(p *peer) { p.frame(true, opText, make([]byte, 2048)) }, ErrMessageTooBig},
		"oversized fragments": {func(p *peer) {
			p.frame(false, opText, make([]byte, 600))
			p.frame(true, opContinuation, make([]byte, 600))
		}, ErrMessageTooBig},
		"forged huge length": {func(p *peer) {
			_, _ = p.rw.Write([]byte{0x81, 127, 0x3F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
			_ = p.rw.Flush()
		}, ErrMessageTooBig},
		"masked server frame": {func(p *peer) {
			_, _ = p.rw.Write([]byte{0x81, 0x81, 1, 2, 3, 4, 'x' ^ 1})
			_ = p.rw.Flush()
		}, ErrProtocol},
		"reserved bit":        {func(p *peer) { p.frame(true, opText|0x40, []byte("x")) }, ErrProtocol},
		"invalid utf8":        {func(p *peer) { p.frame(true, opText, []byte{0xff, 0xfe}) }, ErrProtocol},
		"orphan continuation": {func(p *peer) { p.frame(true, opContinuation, []byte("x")) }, ErrProtocol},
		"fragmented control":  {func(p *peer) { p.frame(false, opPing, []byte("x")) }, ErrProtocol},
		"unknown opcode":      {func(p *peer) { p.frame(true, 0x3, []byte("x")) }, ErrProtocol},
		"bad close code":      {func(p *peer) { p.frame(true, opClose, []byte{0x03, 0xED}) }, ErrProtocol}, // 1005 on the wire
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(upgradeHandler(t, serverOpts{}, func(p *peer) {
				tc.script(p)
				_, _, _ = p.read() // the client's close frame
			}))
			defer srv.Close()
			c, err := Dial(context.Background(), Options{URL: srv.URL, AllowLoopbackHTTP: true, MaxMessageBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _, err = c.ReadMessage()
			if tc.want == ErrProtocol && errors.Is(err, ErrMessageTooBig) || !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReadDeadline(t *testing.T) {
	srv := httptest.NewServer(upgradeHandler(t, serverOpts{}, func(p *peer) { time.Sleep(500 * time.Millisecond) }))
	defer srv.Close()
	c, err := dialPlain(t, srv, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, _, err = c.ReadMessage()
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("want timeout, got %v", err)
	}
}
