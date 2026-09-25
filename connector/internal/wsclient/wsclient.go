// Package wsclient is a minimal RFC 6455 WebSocket client built on the standard
// library only. It supports exactly what the coordinator's event stream needs:
// ws:// and wss:// dialing, masked client frames, text/ping/pong/close handling,
// fragmented messages, and a bounded message size. There is no extension or
// subprotocol negotiation; a server that tries to negotiate one is rejected.
package wsclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
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

// DefaultMaxMessage bounds one reassembled message.
const DefaultMaxMessage = 64 << 10

// maxHandshakeBytes bounds the server's HTTP upgrade response.
const maxHandshakeBytes = 16 << 10

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// MessageType identifies a data message returned by ReadMessage.
type MessageType int

const (
	TextMessage   MessageType = opText
	BinaryMessage MessageType = opBinary
)

// Close status codes used by this package.
const (
	CloseNormal        = 1000
	CloseGoingAway     = 1001
	CloseProtocolError = 1002
	CloseNoStatus      = 1005 // never sent; reported when a close frame had no code
	CloseAbnormal      = 1006 // never sent; reported when the TCP stream ended without a close frame
	CloseInvalidData   = 1007
	CloseTooBig        = 1009
)

// Options configures Dial.
type Options struct {
	// Header is added to the upgrade request (for example Authorization). The
	// WebSocket handshake headers themselves cannot be overridden.
	Header http.Header
	// TLSConfig is cloned for wss:// URLs. ServerName defaults to the URL host
	// and NextProtos is forced to HTTP/1.1. Nil uses the system roots.
	TLSConfig *tls.Config
	// MaxMessage bounds one message; zero means DefaultMaxMessage.
	MaxMessage int
	// WriteTimeout bounds each frame write when no write deadline is set by the
	// caller; zero means 10 s.
	WriteTimeout time.Duration
}

// HandshakeError reports a server that answered the upgrade with something
// other than 101. It carries only the status code.
type HandshakeError struct{ Status int }

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("websocket upgrade rejected (HTTP %d)", e.Status)
}

// CloseError is returned by ReadMessage once the connection is closed. Code is
// the status the server sent, CloseNoStatus when its close frame had none, or
// CloseAbnormal when the stream ended without a close frame.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("websocket closed (%d: %s)", e.Code, e.Reason)
	}
	return fmt.Sprintf("websocket closed (%d)", e.Code)
}

// ErrBadHandshake wraps every upgrade response that is not a valid RFC 6455
// acceptance (other than a non-101 status, which is a *HandshakeError).
var ErrBadHandshake = errors.New("invalid websocket handshake")

// ErrClosed is returned by writes after Close.
var ErrClosed = errors.New("websocket connection closed")

// Conn is a client WebSocket connection. ReadMessage must be called from one
// goroutine at a time; WriteText, Ping and Close are safe for concurrent use.
type Conn struct {
	conn         net.Conn
	br           *bufio.Reader
	maxMessage   int
	writeTimeout time.Duration

	wmu        sync.Mutex
	closeSent  bool
	userWDL    time.Time
	closedOnce sync.Once

	// closeErr is set by the reader once a close frame arrives or the stream
	// fails; subsequent reads return it.
	closeErr error
}

// Dial opens a WebSocket connection. ctx bounds the TCP connect, the TLS
// handshake and the HTTP upgrade; it has no effect on the returned Conn.
func Dial(ctx context.Context, rawURL string, opts Options) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid websocket URL")
	}
	var secure bool
	switch u.Scheme {
	case "ws":
	case "wss":
		secure = true
	default:
		return nil, errors.New("websocket URL must use ws or wss")
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
		if secure {
			port = "443"
		}
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	// Until the upgrade completes, ctx's cancellation or deadline aborts any
	// blocked I/O (and ctx.Err is then already set when the I/O fails).
	stop := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Unix(1, 0)) })
	c, err := handshake(ctx, raw, u, secure, host, opts)
	if stopped := stop(); err != nil || !stopped {
		_ = raw.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			err = context.Canceled
		}
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return c, nil
}

// limitedConnReader bounds how much of the stream the HTTP response parser may
// consume; lifted once the handshake is done.
type limitedConnReader struct {
	r      io.Reader
	remain int
	on     bool
}

func (l *limitedConnReader) Read(p []byte) (int, error) {
	if !l.on {
		return l.r.Read(p)
	}
	if l.remain <= 0 {
		return 0, errors.New("websocket handshake response too large")
	}
	if len(p) > l.remain {
		p = p[:l.remain]
	}
	n, err := l.r.Read(p)
	l.remain -= n
	return n, err
}

func handshake(ctx context.Context, raw net.Conn, u *url.URL, secure bool, host string, opts Options) (*Conn, error) {
	conn := raw
	if secure {
		var cfg *tls.Config
		if opts.TLSConfig != nil {
			cfg = opts.TLSConfig.Clone()
		} else {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		cfg.NextProtos = []string{"http/1.1"}
		tc := tls.Client(raw, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tc
	}
	var keyBytes [16]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		return nil, errors.New("cannot generate websocket key")
	}
	key := base64.StdEncoding.EncodeToString(keyBytes[:])
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
	b.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
	b.WriteString("Sec-WebSocket-Key: " + key + "\r\n")
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	for name, values := range opts.Header {
		canon := http.CanonicalHeaderKey(name)
		switch canon {
		case "Host", "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol":
			continue
		}
		if strings.ContainsAny(canon, "\r\n: ") {
			return nil, errors.New("invalid websocket request header")
		}
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				return nil, errors.New("invalid websocket request header")
			}
			b.WriteString(canon + ": " + v + "\r\n")
		}
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, err
	}
	lim := &limitedConnReader{r: conn, remain: maxHandshakeBytes, on: true}
	br := bufio.NewReaderSize(lim, 4096)
	req := &http.Request{Method: http.MethodGet, URL: u}
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable response", ErrBadHandshake)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, &HandshakeError{Status: resp.StatusCode}
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !headerHasToken(resp.Header, "Connection", "upgrade") {
		return nil, fmt.Errorf("%w: missing upgrade headers", ErrBadHandshake)
	}
	sum := sha1.Sum([]byte(key + acceptGUID))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("%w: accept mismatch", ErrBadHandshake)
	}
	if resp.Header.Get("Sec-WebSocket-Extensions") != "" || resp.Header.Get("Sec-WebSocket-Protocol") != "" {
		return nil, fmt.Errorf("%w: unrequested extension or protocol", ErrBadHandshake)
	}
	lim.on = false
	max := opts.MaxMessage
	if max <= 0 {
		max = DefaultMaxMessage
	}
	wt := opts.WriteTimeout
	if wt <= 0 {
		wt = 10 * time.Second
	}
	return &Conn{conn: conn, br: br, maxMessage: max, writeTimeout: wt}, nil
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// SetReadDeadline bounds the next reads (including control frames).
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// SetWriteDeadline replaces the per-write timeout with an absolute deadline;
// the zero time restores the per-write timeout.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.userWDL = t
	return nil
}

// WriteText sends one unfragmented text message.
func (c *Conn) WriteText(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("websocket text must be valid UTF-8")
	}
	return c.writeFrame(opText, []byte(s))
}

// Ping sends a ping control frame (payload at most 125 bytes).
func (c *Conn) Ping(payload []byte) error {
	if len(payload) > 125 {
		return errors.New("websocket control payload too large")
	}
	return c.writeFrame(opPing, payload)
}

func (c *Conn) writeFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closeSent {
		return ErrClosed
	}
	if op == opClose {
		c.closeSent = true
	}
	return c.writeLocked(op, payload)
}

func (c *Conn) writeLocked(op byte, payload []byte) error {
	n := len(payload)
	frame := make([]byte, 0, 14+n)
	frame = append(frame, 0x80|op)
	switch {
	case n <= 125:
		frame = append(frame, 0x80|byte(n))
	case n <= 0xFFFF:
		frame = append(frame, 0x80|126, byte(n>>8), byte(n))
	default:
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return errors.New("cannot generate websocket mask")
	}
	frame = append(frame, mask[:]...)
	start := len(frame)
	frame = append(frame, payload...)
	for i := range payload {
		frame[start+i] ^= mask[i&3]
	}
	dl := c.userWDL
	if dl.IsZero() {
		dl = time.Now().Add(c.writeTimeout)
	}
	_ = c.conn.SetWriteDeadline(dl)
	_, err := c.conn.Write(frame)
	return err
}

// Close sends a close frame with code (when one has not been sent already)
// and closes the underlying connection. It is safe to call more than once and
// concurrently with ReadMessage, which then returns an error.
func (c *Conn) Close(code int) error {
	var err error
	c.closedOnce.Do(func() {
		c.wmu.Lock()
		if !c.closeSent {
			c.closeSent = true
			payload := []byte{byte(code >> 8), byte(code)}
			if code == 0 {
				payload = nil
			}
			c.userWDL = time.Now().Add(time.Second)
			_ = c.writeLocked(opClose, payload)
		}
		c.wmu.Unlock()
		err = c.conn.Close()
	})
	return err
}

type frameHeader struct {
	fin    bool
	op     byte
	length uint64
}

func (c *Conn) readHeader() (frameHeader, error) {
	var h frameHeader
	var b [2]byte
	if _, err := io.ReadFull(c.br, b[:]); err != nil {
		return h, err
	}
	if b[0]&0x70 != 0 {
		return h, c.protocolFail(CloseProtocolError, "reserved bits set")
	}
	h.fin = b[0]&0x80 != 0
	h.op = b[0] & 0x0F
	if b[1]&0x80 != 0 {
		return h, c.protocolFail(CloseProtocolError, "masked server frame")
	}
	switch l := b[1] & 0x7F; l {
	case 126:
		var e [2]byte
		if _, err := io.ReadFull(c.br, e[:]); err != nil {
			return h, err
		}
		h.length = uint64(binary.BigEndian.Uint16(e[:]))
		if h.length < 126 {
			return h, c.protocolFail(CloseProtocolError, "non-minimal length")
		}
	case 127:
		var e [8]byte
		if _, err := io.ReadFull(c.br, e[:]); err != nil {
			return h, err
		}
		h.length = binary.BigEndian.Uint64(e[:])
		if h.length>>63 != 0 || h.length <= 0xFFFF {
			return h, c.protocolFail(CloseProtocolError, "invalid length")
		}
	default:
		h.length = uint64(l)
	}
	return h, nil
}

// protocolFail sends a close frame with code and returns the matching error,
// which the reader also remembers.
func (c *Conn) protocolFail(code int, reason string) error {
	_ = c.Close(code)
	c.closeErr = fmt.Errorf("websocket protocol error: %s", reason)
	return c.closeErr
}

// ReadMessage returns the next complete data message. Pings are answered and
// pongs discarded transparently. When the server closes, ReadMessage returns a
// *CloseError carrying its code, after echoing the close.
func (c *Conn) ReadMessage() (MessageType, []byte, error) {
	if c.closeErr != nil {
		return 0, nil, c.closeErr
	}
	var (
		msgOp   byte
		msg     []byte
		inFrags bool
	)
	for {
		h, err := c.readHeader()
		if err != nil {
			return 0, nil, c.streamFail(err)
		}
		if h.op >= 0x8 { // control frame
			if !h.fin || h.length > 125 {
				return 0, nil, c.protocolFail(CloseProtocolError, "invalid control frame")
			}
			payload := make([]byte, h.length)
			if _, err := io.ReadFull(c.br, payload); err != nil {
				return 0, nil, c.streamFail(err)
			}
			switch h.op {
			case opPing:
				c.wmu.Lock()
				if !c.closeSent {
					err = c.writeLocked(opPong, payload)
				}
				c.wmu.Unlock()
				if err != nil {
					return 0, nil, c.streamFail(err)
				}
			case opPong:
			case opClose:
				return 0, nil, c.handleClose(payload)
			default:
				return 0, nil, c.protocolFail(CloseProtocolError, "unknown control opcode")
			}
			continue
		}
		switch h.op {
		case opText, opBinary:
			if inFrags {
				return 0, nil, c.protocolFail(CloseProtocolError, "new message inside fragmented message")
			}
			msgOp, inFrags = h.op, true
		case opContinuation:
			if !inFrags {
				return 0, nil, c.protocolFail(CloseProtocolError, "unexpected continuation frame")
			}
		default:
			return 0, nil, c.protocolFail(CloseProtocolError, "unknown data opcode")
		}
		if h.length > uint64(c.maxMessage-len(msg)) {
			return 0, nil, c.protocolFail(CloseTooBig, "message too large")
		}
		start := len(msg)
		msg = append(msg, make([]byte, h.length)...)
		if _, err := io.ReadFull(c.br, msg[start:]); err != nil {
			return 0, nil, c.streamFail(err)
		}
		if !h.fin {
			continue
		}
		if msgOp == opText && !utf8.Valid(msg) {
			return 0, nil, c.protocolFail(CloseInvalidData, "invalid UTF-8 text")
		}
		if msg == nil {
			msg = []byte{}
		}
		return MessageType(msgOp), msg, nil
	}
}

func (c *Conn) handleClose(payload []byte) error {
	ce := &CloseError{Code: CloseNoStatus}
	switch {
	case len(payload) == 1:
		_ = c.protocolFail(CloseProtocolError, "invalid close payload")
		return c.closeErr
	case len(payload) >= 2:
		ce.Code = int(binary.BigEndian.Uint16(payload[:2]))
		if !utf8.Valid(payload[2:]) {
			_ = c.protocolFail(CloseProtocolError, "invalid close reason")
			return c.closeErr
		}
		ce.Reason = string(payload[2:])
	}
	// Echo the close (with the same code when it is one a client may send) and
	// drop the TCP stream; the server has finished.
	echo := ce.Code
	if echo == CloseNoStatus || echo == CloseAbnormal || echo == 1015 || echo < 1000 || echo > 4999 {
		echo = 0
	}
	_ = c.Close(echo)
	c.closeErr = ce
	return ce
}

// streamFail records a read failure. A stream that ended without a close
// frame is reported as CloseAbnormal; timeouts and local closes pass through.
func (c *Conn) streamFail(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = &CloseError{Code: CloseAbnormal}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		// A timed-out read leaves the frame stream at an unknown offset.
		_ = c.Close(CloseGoingAway)
	}
	c.closeErr = err
	return err
}
