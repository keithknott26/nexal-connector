package bandwidth

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestServerAndMeasure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := NewServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Wrap: start serving on an already-open listener via the public Start path
	// by using the loopback address.
	ln.Close()

	// Use Start with the loopback address on a random port.
	err = srv.Start(ctx, "127.0.0.1")
	if err != nil {
		// Port 41820 may be in use; skip rather than fail.
		t.Skipf("cannot start bandwidth server on loopback: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("server addr is empty after start")
	}

	// Measure a small payload.
	mbps, elapsed, err := Measure(ctx, "127.0.0.1", 64<<10) // 64 KiB
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if mbps <= 0 {
		t.Errorf("expected positive Mbps, got %f", mbps)
	}
	if elapsed <= 0 {
		t.Errorf("expected positive elapsed, got %v", elapsed)
	}
	t.Logf("loopback bandwidth: %.2f Mbps in %v", mbps, elapsed)
}

func TestServerRejectsOversizedPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv := NewServer()
	err := srv.Start(ctx, "127.0.0.1")
	if err != nil {
		t.Skipf("cannot start bandwidth server: %v", err)
	}
	defer srv.Stop()

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Request more than MaxPayloadBytes.
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], MaxPayloadBytes+1)
	if _, err := conn.Write(sizeBuf[:]); err != nil {
		t.Fatal(err)
	}

	// Server should close the connection without sending data.
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF for oversized request, got %v", err)
	}
}

func TestServerRejectsZeroPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv := NewServer()
	err := srv.Start(ctx, "127.0.0.1")
	if err != nil {
		t.Skipf("cannot start bandwidth server: %v", err)
	}
	defer srv.Stop()

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], 0)
	if _, err := conn.Write(sizeBuf[:]); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF for zero request, got %v", err)
	}
}

func TestMeasureInvalidPayload(t *testing.T) {
	ctx := context.Background()
	_, _, err := Measure(ctx, "127.0.0.1", MaxPayloadBytes+1)
	if err == nil {
		t.Error("expected error for payload exceeding max")
	}
}

func TestServerStopIdempotent(t *testing.T) {
	srv := NewServer()
	srv.Stop() // no-op
	srv.Stop() // still no-op
	if srv.Addr() != "" {
		t.Errorf("expected empty addr, got %q", srv.Addr())
	}
}

func TestTestPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := NewServer()
	err := srv.Start(ctx, "127.0.0.1")
	if err != nil {
		t.Skipf("cannot start bandwidth server: %v", err)
	}
	defer srv.Stop()

	result := TestPeer(ctx, "peer-1", "Test Peer", "127.0.0.1")
	if result.PeerID != "peer-1" {
		t.Errorf("PeerID = %q, want peer-1", result.PeerID)
	}
	if result.PeerName != "Test Peer" {
		t.Errorf("PeerName = %q, want Test Peer", result.PeerName)
	}
	if result.DownloadMbps <= 0 {
		t.Errorf("expected positive DownloadMbps, got %f", result.DownloadMbps)
	}
	if result.MeasuredAt.IsZero() {
		t.Error("MeasuredAt should not be zero")
	}
}
