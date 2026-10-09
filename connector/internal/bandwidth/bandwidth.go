// Package bandwidth implements lightweight peer-to-peer throughput measurement
// over the neXal mesh tunnel. Each connector runs a small TCP server on its
// tunnel address; peers connect, request a download of a known size and measure
// how fast the bytes arrive. The test is deliberately small (2 MB default) to
// avoid saturating a DSL uplink — the owner's connection is the constraint, and
// burning it for measurement defeats the purpose.
//
// The protocol is a single request-response: the client sends a 4-byte big-endian
// payload length (capped at MaxPayloadBytes), the server streams that many random
// bytes back and closes the connection. No framing, no auth — the tunnel address
// is only reachable through the authenticated mesh, so reachability is the
// credential.
package bandwidth

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"
)

// DefaultPayloadBytes is the download size for a single test. 2 MB takes ~1.6 s
// on a 10 Mb/s DSL uplink (the sender's bottleneck) and ~16 ms on gigabit fibre.
// Large enough that TCP slow-start does not dominate, small enough that even the
// slowest residential link finishes in a few seconds.
const DefaultPayloadBytes = 2 << 20 // 2 MiB

// MaxPayloadBytes is the hard cap the server enforces. Anything above this is
// either a bug or a peer trying to make the server allocate memory. The server
// streams from a reusable buffer rather than allocating the full size, so the cap
// is about wall-clock time, not memory.
const MaxPayloadBytes = 8 << 20 // 8 MiB

// Port is the well-known TCP port for bandwidth tests on the tunnel address.
// Chosen to avoid conflicts with SSH (22), VNC (5900) and SMB (445) which the
// mesh already uses, and to be outside the IANA registered range.
const Port = 41820

// sendBufSize is the per-write chunk the server streams. 64 KiB matches the
// JuiceFS block size and the throttle burst constant.
const sendBufSize = 64 << 10

// Result is one bandwidth measurement to a peer.
type Result struct {
	PeerID        string        `json:"peerId"`
	PeerName      string        `json:"peerName"`
	DownloadMbps  float64       `json:"downloadMbps"`
	UploadMbps    float64       `json:"uploadMbps,omitempty"` // reserved for future use
	PayloadBytes  int           `json:"payloadBytes"`
	Elapsed       time.Duration `json:"elapsed"`
	MeasuredAt    time.Time     `json:"measuredAt"`
	TunnelAddress string        `json:"tunnelAddress"`
}

// Server listens on the tunnel address and serves bandwidth test payloads.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	addr     string
}

// NewServer creates a bandwidth test server but does not start listening.
func NewServer() *Server {
	return &Server{}
}

// Start begins listening on the given tunnel address. It is safe to call from
// any goroutine. If already listening, Stop is called first.
func (s *Server) Start(ctx context.Context, tunnelAddr string) error {
	s.Stop()
	listenAddr := net.JoinHostPort(tunnelAddr, fmt.Sprintf("%d", Port))
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("bandwidth server: %w", err)
	}
	s.mu.Lock()
	s.listener = ln
	s.addr = listenAddr
	s.mu.Unlock()
	go s.serve(ctx, ln)
	return nil
}

// Stop shuts down the listener. Idempotent.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.addr = ""
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// Addr returns the address the server is listening on, or "" if not running.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

func (s *Server) serve(ctx context.Context, ln net.Listener) {
	// Reusable buffer of random bytes. Re-randomising per connection is
	// unnecessary: the purpose is to defeat compression, not to be
	// cryptographically unpredictable.
	buf := make([]byte, sendBufSize)
	if _, err := rand.Read(buf); err != nil {
		return
	}
	sem := make(chan struct{}, 4) // at most 4 concurrent tests
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Listener closed via Stop().
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			s.handleConn(conn, buf)
		}()
	}
}

func (s *Server) handleConn(conn net.Conn, buf []byte) {
	defer conn.Close()
	// 30 s deadline for the entire exchange; matches the client side so slow links can complete.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Read 4-byte big-endian payload length.
	var sizeBuf [4]byte
	if _, err := io.ReadFull(conn, sizeBuf[:]); err != nil {
		return
	}
	requested := binary.BigEndian.Uint32(sizeBuf[:])
	if requested == 0 || requested > MaxPayloadBytes {
		return
	}

	// Stream the payload in chunks from the pre-filled buffer.
	remaining := int(requested)
	for remaining > 0 {
		n := len(buf)
		if n > remaining {
			n = remaining
		}
		written, err := conn.Write(buf[:n])
		remaining -= written
		if err != nil {
			return
		}
	}
}

// Measure performs a download bandwidth test to a peer's tunnel address.
// It connects to the peer's bandwidth server, requests payloadBytes of data,
// and measures the download speed.
func Measure(ctx context.Context, tunnelAddr string, payloadBytes int) (float64, time.Duration, error) {
	if payloadBytes <= 0 {
		payloadBytes = DefaultPayloadBytes
	}
	if payloadBytes > MaxPayloadBytes {
		return 0, 0, errors.New("payload exceeds maximum")
	}

	addr := net.JoinHostPort(tunnelAddr, fmt.Sprintf("%d", Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, 0, fmt.Errorf("bandwidth test dial: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Send 4-byte payload length request.
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], uint32(payloadBytes))
	if _, err := conn.Write(sizeBuf[:]); err != nil {
		return 0, 0, fmt.Errorf("bandwidth test write: %w", err)
	}

	// Read the full payload, measuring elapsed time.
	start := time.Now()
	received := 0
	buf := make([]byte, sendBufSize)
	for received < payloadBytes {
		n, err := conn.Read(buf)
		received += n
		if err != nil {
			if err == io.EOF && received >= payloadBytes {
				break
			}
			return 0, 0, fmt.Errorf("bandwidth test read: %w (got %d/%d)", err, received, payloadBytes)
		}
	}
	elapsed := time.Since(start)

	if elapsed <= 0 {
		return 0, 0, errors.New("bandwidth test: zero elapsed time")
	}

	// Calculate Mbps: (bytes * 8) / (seconds * 1_000_000)
	mbps := float64(received) * 8 / elapsed.Seconds() / 1_000_000
	mbps = math.Round(mbps*100) / 100 // 2 decimal places
	return mbps, elapsed, nil
}

// TestPeer runs a bandwidth test to one peer and returns a Result.
func TestPeer(ctx context.Context, peerID, peerName, tunnelAddr string) Result {
	mbps, elapsed, err := Measure(ctx, tunnelAddr, DefaultPayloadBytes)
	result := Result{
		PeerID:        peerID,
		PeerName:      peerName,
		TunnelAddress: tunnelAddr,
		PayloadBytes:  DefaultPayloadBytes,
		MeasuredAt:    time.Now(),
	}
	if err == nil {
		result.DownloadMbps = mbps
		result.Elapsed = elapsed
	}
	return result
}
