// Package wsclient is a deliberately small RFC 6455 WebSocket CLIENT.
//
// It exists because the connector is stdlib-only and the coordinator's live
// presence feed (GET /api/v2/hosts/events) is a WebSocket. It implements exactly
// what that feed needs and nothing more:
//
//   - one HTTP/1.1 Upgrade handshake, with the Sec-WebSocket-Accept proof
//     verified (RFC 6455 §4.2.2) so a proxy or origin that merely answers 101
//     without speaking WebSocket is refused;
//   - client-side masking of every frame with a fresh crypto/rand key (§5.3);
//   - text, binary, close, ping and pong frames, with fragmented messages
//     reassembled and control frames accepted between fragments (§5.4);
//   - a hard per-message size cap, so a hostile or broken server cannot make
//     this process buffer without bound;
//   - UTF-8 validation of text messages (§8.1).
//
// It deliberately does NOT implement extensions (permessage-deflate: a
// compression oracle next to a bearer token is not a feature worth having),
// subprotocols, or a server. A server that tries to negotiate either extension
// or subprotocol is refused, because we never offered one.
//
// ORIGIN POLICY. This package does not invent a policy of its own. The caller
// hands it an http(s) URL; https is always allowed, and plain http is allowed
// only when Options.AllowLoopbackHTTP is set AND the host is a numeric loopback
// address. That is the same rule config.ValidateURL applies to the coordinator
// origin (http only for an explicit development numeric-loopback profile), and
// internal/client is the only production caller: it passes its already
// validated origin and its dev flag. TLS mirrors internal/client too: a TLS 1.3
// floor, CurvePreferences left nil (see the long note in client.New for why that
// must stay nil), no ambient proxy, and no redirects — a 3xx is a failed
// handshake, not something to follow with a bearer token attached.
//
// ERRORS never contain the URL, the headers or any server-supplied text other
// than a close frame's reason, which is length-bounded by the protocol and is
// returned only inside CloseError for the caller to log or drop.
package wsclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec -- RFC 6455 §4.2.2 mandates SHA-1 for the Accept proof; it is not used for security here.
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// MessageType is the data opcode of a complete message.
type MessageType int

const (
	// TextMessage is a UTF-8 text message (opcode 0x1).
	TextMessage MessageType = 1
	// BinaryMessage is a binary message (opcode 0x2).
	BinaryMessage MessageType = 2
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	// acceptGUID is the fixed RFC 6455 §1.3 string concatenated with the key.
	acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// DefaultMaxMessageBytes caps one reassembled message. The presence feed's
	// largest frame is a snapshot of online host ids: at the peer directory's
	// 200-host page and 128-byte ids that is well under 32 KiB, so 64 KiB is
	// headroom, not a budget anyone should grow into.
	DefaultMaxMessageBytes = 64 << 10
	// maxControlPayload is the protocol limit for close/ping/pong (§5.5).
	maxControlPayload = 125
	// maxHandshakeResponse bounds the 101 response headers we will read.
	maxHandshakeResponse = 16 << 10
)

// Standard close codes used by this package (RFC 6455 §7.4.1).
const (
	CloseNormal          = 1000
	CloseGoingAway       = 1001
	CloseProtocolError   = 1002
	CloseUnsupportedData = 1003
	CloseNoStatus        = 1005 // never sent on the wire; reported when a close frame had no code
	CloseInvalidPayload  = 1007
	CloseMessageTooBig   = 1009
)

// Options configures Dial.
type Options struct {
	// URL is the endpoint as an http:// or https:// URL (not ws/wss): callers in
	// this repository hold coordinator origins, and converting at one place here
	// keeps the scheme check in one place too.
	URL string
	// Header carries extra request headers, typically Authorization. Values are
	// checked for CR/LF so a header can never be split.
	Header http.Header
	// AllowLoopbackHTTP permits plain http, and only to a numeric loopback host.
	// It exists for the development profile and tests; production passes false.
	AllowLoopbackHTTP bool
	// TLSConfig optionally supplies RootCAs (tests use httptest's certificate).
	// It is cloned, and MinVersion is forced up to TLS 1.3 regardless.
	TLSConfig *tls.Config
	// HandshakeTimeout bounds dial + TLS + upgrade. Zero means 10 s.
	HandshakeTimeout time.Duration
	// MaxMessageBytes caps one reassembled message. Zero means
	// DefaultMaxMessageBytes.
	MaxMessageBytes int
}

// HandshakeError is a non-101 response to the upgrade request. Like
// client.StatusError it carries the status code and nothing else.
type HandshakeError struct{ Status int }

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("websocket upgrade refused (HTTP %d)", e.Status)
}

// CloseError reports that the peer closed the connection with a close frame.
// Code is CloseNoStatus when the frame carried no code.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("websocket closed by peer (code %d)", e.Code)
}

// ErrProtocol is returned (wrapped) for any framing violation by the server.
var ErrProtocol = errors.New("websocket protocol violation")

// ErrMessageTooBig is returned when a message exceeds the size cap.
var ErrMessageTooBig = errors.New("websocket message exceeds size limit")

// ErrClosed is returned by writes after Close or after the peer closed.
var ErrClosed = errors.New("websocket connection closed")

// Conn is one client connection. ReadMessage must be called from a single
// goroutine; WriteText, Close and the automatic pong/close replies are
// serialized by an internal write lock, so a pinger goroutine may write while
// another goroutine reads.
type Conn struct {
	conn   net.Conn
	br     *bufio.Reader
	max    int
	wmu    sync.Mutex
	closed bool // guarded by wmu: a close frame has been sent
}

// Dial performs the opening handshake and returns a ready connection.
func Dial(ctx context.Context, opts Options) (*Conn, error) {
	u, err := url.Parse(opts.URL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(opts.URL, "\\\r\n\t ") {
		return nil, errors.New("invalid websocket URL")
	}
	secure := false
	switch u.Scheme {
	case "https":
		secure = true
	case "http":
		ip := net.ParseIP(u.Hostname())
		if !opts.AllowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("websocket requires HTTPS; HTTP is allowed only for explicit development numeric loopback")
		}
	default:
		return nil, errors.New("websocket URL must be http(s)")
	}
	for name, values := range opts.Header {
		for _, v := range values {
			if strings.ContainsAny(name, "\r\n: ") || strings.ContainsAny(v, "\r\n") {
				return nil, errors.New("invalid websocket request header")
			}
		}
	}
	timeout := opts.HandshakeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	max := opts.MaxMessageBytes
	if max <= 0 {
		max = DefaultMaxMessageBytes
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	host := u.Host
	if u.Port() == "" {
		if secure {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	// No proxy: like internal/client, ambient proxy settings must not receive
	// the host bearer token. net.Dialer is used directly, so there is nothing to
	// disable — no environment variable is consulted.
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	conn := raw
	if secure {
		cfg := &tls.Config{}
		if opts.TLSConfig != nil {
			cfg = opts.TLSConfig.Clone()
		}
		// TLS 1.3 floor, as internal/client. CurvePreferences is left exactly as
		// supplied (nil in production) so Go keeps negotiating the hybrid ML-KEM
		// groups; see client.New.
		cfg.MinVersion = tls.VersionTLS13
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		// Offer only HTTP/1.1 over ALPN. An origin that would otherwise pick h2
		// cannot then answer an HTTP/1.1 Upgrade on the same connection.
		cfg.NextProtos = []string{"http/1.1"}
		tc := tls.Client(raw, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		conn = tc
	}
	// The context deadline must also bound the raw reads and writes of the
	// upgrade exchange, which do not observe ctx on their own.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	c, err := handshake(conn, u, opts.Header, max)
	stop()
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil && !isHandshakeError(err) {
			return nil, ctx.Err()
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

func isHandshakeError(err error) bool {
	var h *HandshakeError
	return errors.As(err, &h)
}

func handshake(conn net.Conn, u *url.URL, header http.Header, max int) (*Conn, error) {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, errors.New("cannot generate websocket key")
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	var b strings.Builder
	b.WriteString("GET " + path + " HTTP/1.1\r\n")
	b.WriteString("Host: " + u.Host + "\r\n")
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	b.WriteString("Sec-WebSocket-Key: " + key + "\r\n")
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	for name, values := range header {
		canonical := http.CanonicalHeaderKey(name)
		switch canonical {
		case "Host", "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version",
			"Sec-Websocket-Extensions", "Sec-Websocket-Protocol":
			// The handshake headers are ours alone; a caller cannot override them.
			continue
		}
		for _, v := range values {
			b.WriteString(canonical + ": " + v + "\r\n")
		}
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(io.LimitReader(conn, maxHandshakeResponse), 4096)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		// No body read and no Location followed: a redirect is a failure, the
		// same as internal/client's CheckRedirect.
		return nil, &HandshakeError{Status: resp.StatusCode}
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !headerHasToken(resp.Header, "Connection", "upgrade") {
		return nil, fmt.Errorf("%w: upgrade response missing Upgrade/Connection", ErrProtocol)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != AcceptKey(key) {
		return nil, fmt.Errorf("%w: Sec-WebSocket-Accept mismatch", ErrProtocol)
	}
	if resp.Header.Get("Sec-WebSocket-Extensions") != "" || resp.Header.Get("Sec-WebSocket-Protocol") != "" {
		return nil, fmt.Errorf("%w: server negotiated an extension or subprotocol that was not offered", ErrProtocol)
	}
	// Anything the server sent after the 101 headers is already frame data and
	// sits in br's buffer. The limited reader above only bounded the handshake,
	// so re-wrap: keep what is buffered, then read the raw connection unbounded
	// (message size is capped per message instead).
	buffered, _ := br.Peek(br.Buffered())
	rest := io.MultiReader(strings.NewReader(string(buffered)), conn)
	return &Conn{conn: conn, br: bufio.NewReaderSize(rest, 4096), max: max}, nil
}

// AcceptKey computes the Sec-WebSocket-Accept value for a client key.
func AcceptKey(key string) string {
	h := sha1.Sum([]byte(key + acceptGUID)) // #nosec -- mandated by RFC 6455
	return base64.StdEncoding.EncodeToString(h[:])
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// SetReadDeadline bounds the next ReadMessage. The presence loop sets it after
// every message so a silently dead TCP path is noticed instead of hanging.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// ReadMessage returns the next complete data message. Pings are answered with
// pongs and pongs are discarded without returning. A close frame is answered
// (echoing its code) and returned as *CloseError. On a protocol violation the
// connection is closed with the matching code and an error wrapping
// ErrProtocol or ErrMessageTooBig is returned.
func (c *Conn) ReadMessage() (MessageType, []byte, error) {
	var (
		msg     []byte
		msgType MessageType
		inFrag  bool
	)
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil && !errors.Is(err, ErrClosed) {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			code, reason := CloseNoStatus, ""
			if len(payload) == 1 {
				return 0, nil, c.fail(CloseProtocolError, "close frame with 1-byte payload")
			}
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload[:2]))
				if !validReceivedCloseCode(code) || !utf8.Valid(payload[2:]) {
					return 0, nil, c.fail(CloseProtocolError, "invalid close frame")
				}
				reason = string(payload[2:])
			}
			// Echo the close (§5.5.1) and drop the TCP connection: the server
			// initiated, so it is responsible for closing its side.
			echo := CloseNormal
			if code != CloseNoStatus {
				echo = code
			}
			_ = c.writeClose(echo, "")
			_ = c.conn.Close()
			return 0, nil, &CloseError{Code: code, Reason: reason}
		case opText, opBinary:
			if inFrag {
				return 0, nil, c.fail(CloseProtocolError, "new data frame inside a fragmented message")
			}
			msgType = MessageType(op)
			msg = append(msg[:0], payload...)
			inFrag = !fin
		case opContinuation:
			if !inFrag {
				return 0, nil, c.fail(CloseProtocolError, "continuation without a message")
			}
			if len(msg)+len(payload) > c.max {
				return 0, nil, c.fail(CloseMessageTooBig, "message too big")
			}
			msg = append(msg, payload...)
			inFrag = !fin
		default:
			return 0, nil, c.fail(CloseProtocolError, "unknown opcode")
		}
		if !inFrag {
			if msgType == TextMessage && !utf8.Valid(msg) {
				return 0, nil, c.fail(CloseInvalidPayload, "text message is not UTF-8")
			}
			return msgType, msg, nil
		}
	}
}

// readFrame reads one frame and unmasks nothing: servers must not mask (§5.1).
func (c *Conn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin = h[0]&0x80 != 0
	if h[0]&0x70 != 0 {
		// No extension was negotiated, so every RSV bit must be zero.
		return false, 0, nil, c.fail(CloseProtocolError, "reserved bits set")
	}
	op = h[0] & 0x0F
	if h[1]&0x80 != 0 {
		return false, 0, nil, c.fail(CloseProtocolError, "server frame is masked")
	}
	length := uint64(h[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
		if length < 126 {
			return false, 0, nil, c.fail(CloseProtocolError, "non-minimal length")
		}
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
		if length>>63 != 0 || length <= 0xFFFF {
			return false, 0, nil, c.fail(CloseProtocolError, "invalid 64-bit length")
		}
	}
	if op >= 0x8 {
		if !fin || length > maxControlPayload {
			return false, 0, nil, c.fail(CloseProtocolError, "invalid control frame")
		}
	} else if length > uint64(c.max) {
		// Checked BEFORE allocating, so a forged 2^62 length allocates nothing.
		return false, 0, nil, c.fail(CloseMessageTooBig, "frame too big")
	}
	payload = make([]byte, int(length))
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	return fin, op, payload, nil
}

// validReceivedCloseCode applies §7.4: 1000–1003, 1007–1011 and the
// registered/application ranges 3000–4999 may appear on the wire; 1004–1006,
// 1015 and anything below 1000 may not.
func validReceivedCloseCode(code int) bool {
	switch {
	case code >= 1000 && code <= 1003:
		return true
	case code >= 1007 && code <= 1014:
		return true
	case code >= 3000 && code <= 4999:
		return true
	}
	return false
}

// fail sends a close with code (best effort), closes the socket and returns an
// error describing the violation without any server-supplied bytes.
func (c *Conn) fail(code int, why string) error {
	_ = c.writeClose(code, "")
	_ = c.conn.Close()
	if code == CloseMessageTooBig {
		return fmt.Errorf("%w: %s", ErrMessageTooBig, why)
	}
	return fmt.Errorf("%w: %s", ErrProtocol, why)
}

// WriteText sends one unfragmented, masked text frame.
func (c *Conn) WriteText(data []byte) error {
	if len(data) > c.max {
		return ErrMessageTooBig
	}
	if !utf8.Valid(data) {
		return errors.New("websocket text must be UTF-8")
	}
	return c.writeFrame(opText, data)
}

// Close sends a normal close frame and closes the socket. It does not wait for
// the server's echo: every caller in this repository closes only to stop, and
// a stuck peer must not be able to delay shutdown.
func (c *Conn) Close() error {
	_ = c.writeClose(CloseNormal, "")
	return c.conn.Close()
}

func (c *Conn) writeClose(code int, reason string) error {
	if len(reason) > maxControlPayload-2 {
		reason = reason[:maxControlPayload-2]
	}
	p := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(p, uint16(code))
	copy(p[2:], reason)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.closed = true
	return c.writeFrameLocked(opClose, p)
}

func (c *Conn) writeFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrClosed
	}
	return c.writeFrameLocked(op, payload)
}

// writeFrameLocked writes a single FIN frame with a fresh masking key. A write
// deadline bounds it so a peer that stops reading cannot block a writer (and,
// through the lock, every other writer) forever.
func (c *Conn) writeFrameLocked(op byte, payload []byte) error {
	header := make([]byte, 0, 14)
	header = append(header, 0x80|op)
	n := len(payload)
	switch {
	case n <= 125:
		header = append(header, 0x80|byte(n))
	case n <= 0xFFFF:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return errors.New("cannot generate websocket mask")
	}
	header = append(header, mask[:]...)
	frame := make([]byte, len(header)+n)
	copy(frame, header)
	for i := 0; i < n; i++ {
		frame[len(header)+i] = payload[i] ^ mask[i%4]
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(frame)
	return err
}
