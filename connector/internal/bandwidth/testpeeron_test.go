package bandwidth

import (
	"context"
	"net"
	"testing"
)

// TestPeerOnRecordsFailureInResultError: a refused connection must surface as
// Result.Error with zero throughput, while the identifying fields are kept.
func TestPeerOnRecordsFailureInResultError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens on port any more

	r := TestPeerOn(context.Background(), "peer-x", "Peer X", "127.0.0.1", port)
	if r.Error == "" {
		t.Fatal("expected Error to be set when the dial fails")
	}
	if r.DownloadMbps != 0 || r.Elapsed != 0 {
		t.Errorf("failed result should have no throughput, got %v Mbps / %v", r.DownloadMbps, r.Elapsed)
	}
	if r.PeerID != "peer-x" || r.PeerName != "Peer X" || r.TunnelAddress != "127.0.0.1" {
		t.Errorf("identity fields not preserved: %+v", r)
	}
	if r.PayloadBytes != DefaultPayloadBytes {
		t.Errorf("PayloadBytes = %d, want %d", r.PayloadBytes, DefaultPayloadBytes)
	}
	if r.MeasuredAt.IsZero() {
		t.Error("MeasuredAt should be set even on failure")
	}
}
