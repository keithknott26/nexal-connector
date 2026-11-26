package bandwidth

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

// fakeServer accepts one connection, reads the 4-byte length request, then runs serve.
// It returns the listening port and a channel yielding the requested length.
func fakeServer(t *testing.T, serve func(net.Conn)) (int, <-chan uint32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	requested := make(chan uint32, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var buf [4]byte
		if _, err := io.ReadFull(conn, buf[:]); err != nil {
			return
		}
		requested <- binary.BigEndian.Uint32(buf[:])
		serve(conn)
	}()
	return ln.Addr().(*net.TCPAddr).Port, requested
}

func TestMeasureOnTruncatedStreamReportsProgress(t *testing.T) {
	port, _ := fakeServer(t, func(c net.Conn) { _, _ = c.Write(make([]byte, 10)) })
	_, _, err := MeasureOn(context.Background(), "127.0.0.1", port, 1000)
	if err == nil || !strings.Contains(err.Error(), "got 10/1000") {
		t.Fatalf("want read error with progress 10/1000, got %v", err)
	}
}

func TestMeasureOnDefaultsNonPositivePayloadAndRejectsOversize(t *testing.T) {
	port, requested := fakeServer(t, func(c net.Conn) {})
	// The fake server closes without sending, so the call fails, but the request
	// it sent must carry the default size.
	_, _, _ = MeasureOn(context.Background(), "127.0.0.1", port, 0)
	if got := <-requested; got != uint32(DefaultPayloadBytes) {
		t.Fatalf("requested %d, want default %d", got, DefaultPayloadBytes)
	}
	if _, _, err := MeasureOn(context.Background(), "127.0.0.1", port, MaxPayloadBytes+1); err == nil {
		t.Fatal("oversize payload accepted")
	}
}

func TestMeasureOnDialFailureIsWrapped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens on this port now
	_, _, err = MeasureOn(context.Background(), "127.0.0.1", port, 100)
	if err == nil || !strings.Contains(err.Error(), "bandwidth test dial") {
		t.Fatalf("want wrapped dial error for port %s, got %v", strconv.Itoa(port), err)
	}
}
