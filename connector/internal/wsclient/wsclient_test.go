package wsclient

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/tls"
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

// serverFunc runs the server side after a successful upgrade.
type serverFunc func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter)

type upgradeOpts struct {
	status     int
	badAccept  bool
	extensions bool
	noUpgrade  bool
}

func wsServer(t *testing.T, tlsServer bool, o upgradeOpts, fn serverFunc) (*httptest.Server, string) {
	t.Helper()
	done := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Sec-WebSocket-Version") != "13" ||
			!strings.EqualFold(r.Header.Get("Connection"), "Upgrade") {
			t.Errorf("bad upgrade request headers: %v", r.Header)
		}
		if o.status != 0 {
			w.WriteHeader(o.status)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		key := r.Header.Get("Sec-WebSocket-Key")
		if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
			t.Errorf("bad key %q", key)
		}
		sum := sha1.Sum([]byte(key + acceptGUID))
		accept := base64.StdEncoding.EncodeToString(sum[:])
		if o.badAccept {
			accept = "AAAA" + accept[4:]
		}
		resp := "HTTP/1.1 101 Switching Protocols\r\n"
		if !o.noUpgrade {
			resp += "Upgrade: websocket\r\nConnection: Upgrade\r\n"
		}
		resp += "Sec-WebSocket-Accept: " + accept + "\r\n"
		if o.extensions {
			resp += "Sec-WebSocket-Extensions: permessage-deflate\r\n"
		}
		resp += "\r\n"
		_, _ = rw.WriteString(resp)
		_ = rw.Flush()
		if fn != nil {
			fn(t, conn, rw)
		}
		close(done)
	})
	var srv *httptest.Server
	scheme := "ws"
	if tlsServer {
		srv = httptest.NewTLSServer(h)
		scheme = "wss"
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, scheme + strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http"), "s") + "/events"
}

func writeFrame(t *testing.T, rw *bufio.ReadWriter, fin bool, op byte, payload []byte, masked bool) {
	t.Helper()
	b0 := op
	if fin {
		b0 |= 0x80
	}
	hdr := []byte{b0}
	m := byte(0)
	if masked {
		m = 0x80
	}
	n := len(payload)
	switch {
	case n <= 125:
		hdr = append(hdr, m|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, m|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, m|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	if masked {
		hdr = append(hdr, 1, 2, 3, 4)
		p := append([]byte(nil), payload...)
		for i := range p {
			p[i] ^= []byte{1, 2, 3, 4}[i&3]
		}
		payload = p
	}
	_, _ = rw.Write(hdr)
	_, _ = rw.Write(payload)
	if err := rw.Flush(); err != nil {
		t.Logf("server write: %v", err)
	}
}

// readFrame reads one client frame and requires it to be masked.
func readFrame(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) (byte, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var b [2]byte
	if _, err := io.ReadFull(rw, b[:]); err != nil {
		t.Errorf("server read: %v", err)
		return 0, nil
	}
	if b[0]&0x80 == 0 {
		t.Error("client sent a fragmented frame")
	}
	if b[1]&0x80 == 0 {
		t.Error("client frame not masked")
	}
	n := uint64(b[1] & 0x7F)
	switch n {
	case 126:
		var e [2]byte
		_, _ = io.ReadFull(rw, e[:])
		n = uint64(binary.BigEndian.Uint16(e[:]))
	case 127:
		var e [8]byte
		_, _ = io.ReadFull(rw, e[:])
		n = binary.BigEndian.Uint64(e[:])
	}
	var mask [4]byte
	_, _ = io.ReadFull(rw, mask[:])
	p := make([]byte, n)
	_, _ = io.ReadFull(rw, p)
	for i := range p {
		p[i] ^= mask[i&3]
	}
	return b[0] & 0x0F, p
}

func closeCode(p []byte) int {
	if len(p) < 2 {
		return 0
	}
	return int(binary.BigEndian.Uint16(p))
}

func dial(t *testing.T, url string, o Options) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, url, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(CloseNormal) })
	return c
}

func TestTextRoundTripAndHeaders(t *testing.T) {
	_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		op, p := readFrame(t, conn, rw)
		if op != opText || string(p) != "ping" {
			t.Errorf("got op %d %q", op, p)
		}
		writeFrame(t, rw, true, opText, []byte("pong"), false)
		op, p = readFrame(t, conn, rw)
		if op != opClose || closeCode(p) != CloseNormal {
			t.Errorf("close op %d code %d", op, closeCode(p))
		}
	})
	c := dial(t, url, Options{Header: http.Header{"Authorization": {"Bearer tok"}}})
	if err := c.WriteText("ping"); err != nil {
		t.Fatal(err)
	}
	typ, msg, err := c.ReadMessage()
	if err != nil || typ != TextMessage || string(msg) != "pong" {
		t.Fatalf("%v %q %v", typ, msg, err)
	}
	if err := c.Close(CloseNormal); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteText("x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestAuthorizationHeaderSent(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization") + "|" + r.URL.Path
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v2/hosts/events",
		Options{Header: http.Header{"Authorization": {"Bearer tok"}}})
	var he *HandshakeError
	if !errors.As(err, &he) || he.Status != 401 {
		t.Fatalf("err %v", err)
	}
	if v := <-got; v != "Bearer tok|/api/v2/hosts/events" {
		t.Fatal(v)
	}
	if strings.Contains(err.Error(), "tok") {
		t.Fatal("error leaks request data")
	}
}

func TestHandshakeRejections(t *testing.T) {
	for name, o := range map[string]upgradeOpts{
		"bad accept": {badAccept: true},
		"extensions": {extensions: true},
		"no upgrade": {noUpgrade: true},
		"status 200": {status: 200},
	} {
		t.Run(name, func(t *testing.T) {
			_, url := wsServer(t, false, o, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := Dial(ctx, url, Options{})
			if err == nil {
				c.Close(CloseNormal)
				t.Fatal("handshake accepted")
			}
			var he *HandshakeError
			if o.status != 0 && (!errors.As(err, &he) || he.Status != o.status) {
				t.Fatalf("err %v", err)
			}
			if o.status == 0 && !errors.Is(err, ErrBadHandshake) {
				t.Fatalf("err %v", err)
			}
		})
	}
	if _, err := Dial(context.Background(), "http://example.com/", Options{}); err == nil {
		t.Fatal("http scheme accepted")
	}
}

func TestDialHonorsContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(2 * time.Second) // never answer the upgrade
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Dial(ctx, "ws://"+ln.Addr().String()+"/", Options{})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestTLS(t *testing.T) {
	srv, url := wsServer(t, true, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		writeFrame(t, rw, true, opText, []byte("secure"), false)
		readFrame(t, conn, rw)
	})
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c := dial(t, url, Options{TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}})
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "secure" {
		t.Fatalf("%q %v", msg, err)
	}
	// Without the test root the certificate must be refused.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c2, err := Dial(ctx, url, Options{}); err == nil {
		c2.Close(CloseNormal)
		t.Fatal("untrusted certificate accepted")
	}
}

func TestPingAutoPongAndFragmentation(t *testing.T) {
	_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		writeFrame(t, rw, true, opPing, []byte("hb"), false)
		op, p := readFrame(t, conn, rw)
		if op != opPong || string(p) != "hb" {
			t.Errorf("pong op %d %q", op, p)
		}
		writeFrame(t, rw, false, opText, []byte("hel"), false)
		writeFrame(t, rw, true, opPing, nil, false) // control frame between fragments
		writeFrame(t, rw, true, opPong, []byte("x"), false)
		writeFrame(t, rw, false, opContinuation, []byte("lo "), false)
		writeFrame(t, rw, true, opContinuation, []byte("wörld"), false)
		readFrame(t, conn, rw) // pong for the second ping
		writeFrame(t, rw, true, opText, nil, false)
		readFrame(t, conn, rw)
	})
	c := dial(t, url, Options{})
	typ, msg, err := c.ReadMessage()
	if err != nil || typ != TextMessage || string(msg) != "hello wörld" {
		t.Fatalf("%v %q %v", typ, msg, err)
	}
	if _, msg, err = c.ReadMessage(); err != nil || msg == nil || len(msg) != 0 {
		t.Fatalf("empty message %q %v", msg, err)
	}
}

func TestLargeMessageWithinLimit(t *testing.T) {
	big := strings.Repeat("a", DefaultMaxMessage)
	_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		writeFrame(t, rw, false, opText, []byte(big[:40000]), false)
		writeFrame(t, rw, true, opContinuation, []byte(big[40000:]), false)
		op, p := readFrame(t, conn, rw)
		if op != opText || len(p) != 70000 {
			t.Errorf("large client frame op %d len %d", op, len(p))
		}
		readFrame(t, conn, rw)
	})
	c := dial(t, url, Options{})
	if _, msg, err := c.ReadMessage(); err != nil || len(msg) != DefaultMaxMessage {
		t.Fatalf("len %d err %v", len(msg), err)
	}
	if err := c.WriteText(strings.Repeat("b", 70000)); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolViolationsCloseWithCode(t *testing.T) {
	cases := map[string]struct {
		send func(t *testing.T, rw *bufio.ReadWriter)
		code int
	}{
		"oversize": {func(t *testing.T, rw *bufio.ReadWriter) {
			writeFrame(t, rw, false, opText, make([]byte, 40000), false)
			writeFrame(t, rw, true, opContinuation, make([]byte, 40000), false)
		}, CloseTooBig},
		"masked": {func(t *testing.T, rw *bufio.ReadWriter) {
			writeFrame(t, rw, true, opText, []byte("x"), true)
		}, CloseProtocolError},
		"stray continuation": {func(t *testing.T, rw *bufio.ReadWriter) {
			writeFrame(t, rw, true, opContinuation, []byte("x"), false)
		}, CloseProtocolError},
		"interleaved message": {func(t *testing.T, rw *bufio.ReadWriter) {
			writeFrame(t, rw, false, opText, []byte("x"), false)
			writeFrame(t, rw, true, opText, []byte("y"), false)
		}, CloseProtocolError},
		"bad utf8": {func(t *testing.T, rw *bufio.ReadWriter) {
			writeFrame(t, rw, true, opText, []byte{0xff, 0xfe}, false)
		}, CloseInvalidData},
		"reserved bits": {func(t *testing.T, rw *bufio.ReadWriter) {
			_, _ = rw.Write([]byte{0xC1, 0x01, 'x'})
			_ = rw.Flush()
		}, CloseProtocolError},
		"fragmented control": {func(t *testing.T, rw *bufio.ReadWriter) {
			writeFrame(t, rw, false, opPing, nil, false)
		}, CloseProtocolError},
		"unknown opcode": {func(t *testing.T, rw *bufio.ReadWriter) {
			writeFrame(t, rw, true, 0x3, nil, false)
		}, CloseProtocolError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := make(chan int, 1)
			_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
				tc.send(t, rw)
				op, p := readFrame(t, conn, rw)
				if op != opClose {
					t.Errorf("op %d", op)
				}
				got <- closeCode(p)
			})
			c := dial(t, url, Options{})
			_, _, err := c.ReadMessage()
			if err == nil {
				t.Fatal("violation accepted")
			}
			if code := <-got; code != tc.code {
				t.Fatalf("close code %d want %d", code, tc.code)
			}
			if _, _, err2 := c.ReadMessage(); err2 == nil {
				t.Fatal("read after failure succeeded")
			}
		})
	}
}

func TestServerCloseCodeSurfaced(t *testing.T) {
	echo := make(chan int, 1)
	_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		writeFrame(t, rw, true, opClose, append([]byte{0x0F, 0xA1}, "credential gone"...), false) // 4001
		_, p := readFrame(t, conn, rw)
		echo <- closeCode(p)
	})
	c := dial(t, url, Options{})
	_, _, err := c.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != 4001 || ce.Reason != "credential gone" {
		t.Fatalf("err %v", err)
	}
	if code := <-echo; code != 4001 {
		t.Fatalf("echo %d", code)
	}
	if _, _, err = c.ReadMessage(); !errors.As(err, &ce) || ce.Code != 4001 {
		t.Fatalf("second read %v", err)
	}
}

func TestEmptyCloseAndAbruptEOF(t *testing.T) {
	_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		writeFrame(t, rw, true, opClose, nil, false)
	})
	c := dial(t, url, Options{})
	var ce *CloseError
	if _, _, err := c.ReadMessage(); !errors.As(err, &ce) || ce.Code != CloseNoStatus {
		t.Fatalf("err %v", err)
	}
	_, url = wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {})
	c = dial(t, url, Options{})
	if _, _, err := c.ReadMessage(); !errors.As(err, &ce) || ce.Code != CloseAbnormal {
		t.Fatalf("err %v", err)
	}
}

func TestReadDeadline(t *testing.T) {
	release := make(chan struct{})
	_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		<-release
	})
	defer close(release)
	c := dial(t, url, Options{})
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, _, err := c.ReadMessage()
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err %v", err)
	}
}

func TestConcurrentCloseUnblocksReader(t *testing.T) {
	release := make(chan struct{})
	_, url := wsServer(t, false, upgradeOpts{}, func(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
		<-release
	})
	defer close(release)
	c := dial(t, url, Options{})
	errc := make(chan error, 1)
	go func() { _, _, err := c.ReadMessage(); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	_ = c.Close(CloseGoingAway)
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("read succeeded after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader not unblocked by Close")
	}
}
