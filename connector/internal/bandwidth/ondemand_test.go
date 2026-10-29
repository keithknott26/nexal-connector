package bandwidth

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"syscall"
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

// HandleRequest opens the request's slot port on the published tunnel address (and the legacy fixed
// port for older peers), and ignores requests that arrive before the runner has a context or
// without a tunnel address.
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

	t.Run("opens the request's slot port", func(t *testing.T) {
		r := NewRunner(slog.New(slog.DiscardHandler))
		r.ctx, r.provider = ctx, provider
		defer r.server.Stop()
		r.HandleRequest("req-3", "peer-a")
		if r.server.Addr() == "" {
			t.Fatal("port should be open after a request")
		}
		if _, _, err := MeasureOn(ctx, "127.0.0.1", SlotPort("req-3"), 64<<10); err != nil {
			t.Fatalf("MeasureOn slot port: %v", err)
		}
	})

	t.Run("several requests at once each get their own port", func(t *testing.T) {
		r := NewRunner(slog.New(slog.DiscardHandler))
		r.ctx, r.provider = ctx, provider
		defer r.server.Stop()
		ids := []string{}
		seen := map[int]bool{}
		for i := 0; len(ids) < 4; i++ {
			id := "bw_" + strconv.Itoa(i)
			if !seen[SlotPort(id)] && slotPortFree(SlotPort(id)) {
				seen[SlotPort(id)] = true
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			r.HandleRequest(id, "peer-a")
		}
		if got := r.server.OpenSlots(); got < len(ids) {
			t.Fatalf("%d slots open, want at least %d", got, len(ids))
		}
		for _, id := range ids {
			if _, _, err := MeasureOn(ctx, "127.0.0.1", SlotPort(id), 64<<10); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
		}
	})

	t.Run("a slot port that is taken is refused, not stolen", func(t *testing.T) {
		r := NewRunner(slog.New(slog.DiscardHandler))
		r.ctx, r.provider = ctx, provider
		defer r.server.Stop()
		if err := r.server.OpenSlot(ctx, "127.0.0.1", SlotPort("req-5"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := r.server.OpenSlot(ctx, "127.0.0.1", SlotPort("req-5"), time.Minute); !errors.Is(err, ErrSlotBusy) {
			t.Fatalf("second OpenSlot = %v, want ErrSlotBusy", err)
		}
	})
}

// slotPortFree reports whether the port is unused; the slot range sits in the ephemeral range
// on some systems, so a parallel test run can occupy one.
func slotPortFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func TestSlotPortStaysInRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		p := SlotPort("bw_" + strconv.Itoa(i))
		if p < SlotBase || p >= SlotBase+SlotCount || p == Port {
			t.Fatalf("SlotPort = %d outside %d..%d", p, SlotBase, SlotBase+SlotCount-1)
		}
	}
	if SlotPort("x") != SlotPort("x") {
		t.Fatal("SlotPort must be stable for one request id")
	}
}

// With an id-returning opener the test dials the request's slot port and succeeds.
func TestMeasureUsesTheRequestSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRunner(slog.New(slog.DiscardHandler))
	defer r.server.Stop()
	r.SetOpenerWithID(func(ctx context.Context, addr string) (string, error) {
		return "bw_slot_test", r.server.OpenSlot(ctx, "127.0.0.1", SlotPort("bw_slot_test"), time.Minute)
	})
	res := r.measure(ctx, mesh.Peer{ID: "p", Name: "p", TunnelAddress: "127.0.0.1"})
	if res.Error != "" || res.DownloadMbps <= 0 {
		t.Fatalf("measure = %+v, want a successful reading", res)
	}
}

// A port that never opens fails with the kind of failure and the request id, so the log says why.
func TestMeasureNamesWhyThePortDidNotOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := NewRunner(slog.New(slog.DiscardHandler))
	r.SetOpenerWithID(func(context.Context, string) (string, error) { return "bw_nothing_listens", nil })
	res := r.measure(ctx, mesh.Peer{ID: "p", Name: "p", TunnelAddress: "127.0.0.1"})
	for _, want := range []string{"[refused]", "request=bw_nothing_listens", "nothing was listening"} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("error %q does not contain %q", res.Error, want)
		}
	}
}

func TestClassifyDial(t *testing.T) {
	for _, c := range []struct {
		err  error
		kind string
	}{
		{&net.OpError{Err: syscall.ECONNREFUSED}, "refused"},
		{&net.OpError{Err: syscall.EHOSTUNREACH}, "no-route"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{errors.New("boom"), "error"},
	} {
		if kind, hint := classifyDial(c.err); kind != c.kind || hint == "" {
			t.Errorf("classifyDial(%v) = %q, %q; want kind %q with a hint", c.err, kind, hint, c.kind)
		}
	}
	if kind, _ := classifyDial(timeoutErr{}); kind != "timeout" {
		t.Errorf("a net timeout classified as %q", kind)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestDescribeOpenFailureExplainsConflict(t *testing.T) {
	if got := describeOpenFailure(errors.New("coordinator rejected request (HTTP 409)")); !strings.Contains(got, "already has a bandwidth request open") {
		t.Fatal(got)
	}
}

// Hosts are spread across the startup window, the same address always lands in the same place, and
// the periodic delay drifts within +/-10%.
func TestSchedulesAreStaggered(t *testing.T) {
	if startupOffset("") != 0 {
		t.Fatal("no address, no offset")
	}
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		addr := "100.86.0." + strconv.Itoa(i+1)
		o := startupOffset(addr)
		if o < 0 || o >= startupSpread {
			t.Fatalf("offset %s outside the window", o)
		}
		if o != startupOffset(addr) {
			t.Fatal("offset must be stable")
		}
		seen[o] = true
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct offsets across 20 hosts; they are bunching", len(seen))
	}
	for i := 0; i < 200; i++ {
		d := jitter(TestInterval)
		if d < TestInterval*9/10 || d > TestInterval*11/10 {
			t.Fatalf("jitter(%s) = %s, outside +/-10%%", TestInterval, d)
		}
	}
}

// waitPortFree blocks until the fixed bandwidth port can be bound, so a test does not race the
// asynchronous teardown of a previous test's listener.
func waitPortFree(t *testing.T, within time.Duration) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(Port))
	deadline := time.Now().Add(within)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d still in use: %v", Port, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	// Addr is cleared before the listener is actually closed, so also wait until the port can
	// be rebound; otherwise the next test's StartOnce races the close and hits "address in use".
	for {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(Port)))
		if err == nil {
			_ = ln.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port never released: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
