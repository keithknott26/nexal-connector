package bandwidth

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"nexal/connector/internal/mesh"
)

// The port is open only while a requested transfer is in progress: it closes once a
// payload has been served, so nothing lingers between tests.
func TestStartOnceClosesAfterOneTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer()
	if err := srv.StartOnce(ctx, "127.0.0.1", time.Minute); err != nil {
		t.Fatalf("StartOnce: %v", err)
	}
	if srv.Addr() == "" {
		t.Fatal("port should be open after StartOnce")
	}
	if _, _, err := Measure(ctx, "127.0.0.1", 64<<10); err != nil {
		t.Fatalf("Measure: %v", err)
	}
	waitClosed(t, srv, 2*time.Second)
}

// If no transfer ever arrives, the window bounds how long the port stays open.
func TestStartOnceClosesAfterWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer()
	if err := srv.StartOnce(ctx, "127.0.0.1", 150*time.Millisecond); err != nil {
		t.Fatalf("StartOnce: %v", err)
	}
	waitClosed(t, srv, 2*time.Second)
}

// A probe that connects without asking for data must not close the port; the real test
// that follows still has to succeed.
func TestProbeDoesNotConsumeWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer()
	if err := srv.StartOnce(ctx, "127.0.0.1", time.Minute); err != nil {
		t.Fatalf("StartOnce: %v", err)
	}
	if err := waitForPort(ctx, "127.0.0.1", 2*time.Second); err != nil {
		t.Fatalf("waitForPort: %v", err)
	}
	if srv.Addr() == "" {
		t.Fatal("a probe must not close the port")
	}
	if _, _, err := Measure(ctx, "127.0.0.1", 64<<10); err != nil {
		t.Fatalf("Measure after probe: %v", err)
	}
	waitClosed(t, srv, 2*time.Second)
}

// measure asks the peer to open its port before it dials, and reports a clean failure when
// the request itself fails.
func TestMeasureOpensPeerFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRunner(slog.New(slog.DiscardHandler))
	asked := 0
	r.SetOpener(func(ctx context.Context, addr string) error {
		asked++
		// The coordinator knows a peer by the tunnel address it published, not by the mesh peer id.
		if addr != "127.0.0.1" {
			t.Errorf("opener asked for %q, want the peer's tunnel address", addr)
		}
		return r.server.StartOnce(ctx, "127.0.0.1", time.Minute)
	})
	peer := mesh.Peer{ID: "peer-a", Name: "peer-a", TunnelAddress: "127.0.0.1"}
	res := r.measure(ctx, peer)
	if res.Error != "" || res.DownloadMbps <= 0 {
		t.Fatalf("measure = %+v, want a successful reading", res)
	}
	if asked != 1 {
		t.Fatalf("opener called %d times, want 1", asked)
	}
}

func TestMeasureReportsOpenRequestFailure(t *testing.T) {
	r := NewRunner(slog.New(slog.DiscardHandler))
	r.SetOpener(func(context.Context, string) error { return errors.New("coordinator unreachable") })
	res := r.measure(context.Background(), mesh.Peer{ID: "peer-b", Name: "peer-b", TunnelAddress: "127.0.0.1"})
	if res.DownloadMbps != 0 || res.Error == "" {
		t.Fatalf("measure = %+v, want a failure with a reason", res)
	}
}

// HandleRequest opens this Mac's port on the published tunnel address, ignores requests
// that arrive before the runner has a context or without a tunnel address, and drops
// repeats that come faster than minOpenSpacing.
func TestHandleRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := staticProvider{mesh.Status{SelfTunnelAddress: "127.0.0.1"}}

	t.Run("ignored before Run sets the context", func(t *testing.T) {
		r := NewRunner(slog.New(slog.DiscardHandler))
		r.HandleRequest("req-1", "peer-a")
		if r.server.Addr() != "" {
			t.Fatal("port opened without a run context")
		}
	})

	t.Run("ignored without a tunnel address", func(t *testing.T) {
		r := NewRunner(slog.New(slog.DiscardHandler))
		r.ctx, r.provider = ctx, staticProvider{}
		r.HandleRequest("req-2", "peer-a")
		if r.server.Addr() != "" {
			t.Fatal("port opened without a tunnel address")
		}
	})

	t.Run("opens once and rate-limits repeats", func(t *testing.T) {
		r := NewRunner(slog.New(slog.DiscardHandler))
		r.ctx, r.provider = ctx, provider
		r.HandleRequest("req-3", "peer-a")
		if r.server.Addr() == "" {
			t.Fatal("port should be open after a request")
		}
		if _, _, err := Measure(ctx, "127.0.0.1", 64<<10); err != nil {
			t.Fatalf("Measure: %v", err)
		}
		waitClosed(t, r.server, 2*time.Second)
		r.HandleRequest("req-4", "peer-a") // within minOpenSpacing of req-3
		if r.server.Addr() != "" {
			t.Fatal("a repeat request inside minOpenSpacing should be ignored")
		}
	})
}

func waitClosed(t *testing.T, srv *Server, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for srv.Addr() != "" {
		if time.Now().After(deadline) {
			t.Fatal("port still open")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
