package presence

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/wsclient"
)

// fakeConn replays scripted frames, then returns end (or blocks until Close).
type fakeConn struct {
	mu      sync.Mutex
	frames  []string
	end     error
	closed  chan struct{}
	once    sync.Once
	written []string
}

func newFake(end error, frames ...string) *fakeConn {
	return &fakeConn{frames: frames, end: end, closed: make(chan struct{})}
}

func (f *fakeConn) ReadMessage() (wsclient.MessageType, []byte, error) {
	f.mu.Lock()
	if len(f.frames) > 0 {
		m := f.frames[0]
		f.frames = f.frames[1:]
		f.mu.Unlock()
		return wsclient.TextMessage, []byte(m), nil
	}
	end := f.end
	f.mu.Unlock()
	if end != nil {
		return 0, nil, end
	}
	<-f.closed
	return 0, nil, errors.New("closed")
}
func (f *fakeConn) WriteText(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, string(b))
	return nil
}
func (f *fakeConn) SetReadDeadline(time.Time) error { return nil }
func (f *fakeConn) Close() error                    { f.once.Do(func() { close(f.closed) }); return nil }

type wakeCall struct{ macs []string }

type harness struct {
	c      *Client
	mu     sync.Mutex
	sleeps []time.Duration
	wakes  []wakeCall
	dials  int
}

func newHarness(t *testing.T, conns ...func() (Conn, error)) *harness {
	t.Helper()
	h := &harness{}
	var err error
	h.c, err = New(Options{
		HostID: "self",
		Dial: func(ctx context.Context) (Conn, error) {
			h.mu.Lock()
			i := h.dials
			h.dials++
			h.mu.Unlock()
			if i < len(conns) {
				return conns[i]()
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Wake: func(macs []net.HardwareAddr) (int, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			var s []string
			for _, m := range macs {
				s = append(s, m.String())
			}
			h.wakes = append(h.wakes, wakeCall{s})
			return 2, nil
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.mu.Lock()
			h.sleeps = append(h.sleeps, d)
			h.mu.Unlock()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func run(t *testing.T, h *harness, until func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = h.c.Run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for !until() {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestSnapshotAndDeltas(t *testing.T) {
	conn := newFake(nil,
		`{"v":1,"type":"snapshot","online":["b","a","self"],"at":"2026-09-24T00:00:00Z"}`,
		`pong`,
		`{"v":1,"type":"host.online","hostId":"c","at":"x","future":true}`,
		`{"v":1,"type":"host.offline","hostId":"a","at":"x"}`,
		`{"v":1,"type":"host.removed","hostId":"b","at":"x"}`,
		`{"v":1,"type":"host.online","hostId":"bad id!","at":"x"}`,
		`{"v":2,"type":"host.online","hostId":"v2","at":"x"}`,
		`{"v":1,"type":"something.new"}`,
		`not json`,
		`{"v":1,"type":"host.online","hostId":"d","at":"x"}`,
	)
	h := newHarness(t, func() (Conn, error) { return conn, nil })
	want := []string{"c", "d", "self"}
	run(t, h, func() bool { s := h.c.Snapshot(); return s.Connected && reflect.DeepEqual(s.Online, want) })
	s := h.c.Snapshot()
	if s.Connected {
		t.Fatal("still connected after stop")
	}
	if !reflect.DeepEqual(s.Online, want) || s.UpdatedAt.IsZero() {
		t.Fatalf("online set not retained across disconnect: %+v", s)
	}
}

func TestInvalidSnapshotIgnored(t *testing.T) {
	conn := newFake(nil,
		`{"v":1,"type":"snapshot","online":["a"]}`,
		`{"v":1,"type":"snapshot","online":["a","../evil"]}`,
		`{"v":1,"type":"host.online","hostId":"z"}`,
	)
	h := newHarness(t, func() (Conn, error) { return conn, nil })
	run(t, h, func() bool { return reflect.DeepEqual(h.c.Snapshot().Online, []string{"a", "z"}) })
}

func TestRemovedByEventStopsReconnecting(t *testing.T) {
	conn := newFake(nil, `{"v":1,"type":"snapshot","online":["self","x"]}`, `{"v":1,"type":"host.removed","hostId":"self"}`)
	h := newHarness(t, func() (Conn, error) { return conn, nil })
	ctx := context.Background()
	if err := h.c.Run(ctx); err != nil { // returns by itself: no reconnect
		t.Fatal(err)
	}
	s := h.c.Snapshot()
	if !s.Removed || s.Connected || len(s.Online) != 0 || s.LastError == "" {
		t.Fatalf("removal not reflected: %+v", s)
	}
	if h.dials != 1 {
		t.Fatalf("dialed %d times after removal", h.dials)
	}
}

func TestRemovedByCloseCode(t *testing.T) {
	conn := newFake(&wsclient.CloseError{Code: CloseRemoved}, `{"v":1,"type":"snapshot","online":[]}`)
	h := newHarness(t, func() (Conn, error) { return conn, nil })
	if err := h.c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.c.Snapshot().Removed || h.dials != 1 {
		t.Fatal("close 4001 did not stop the loop")
	}
}

func TestSupersededIsNotRemoval(t *testing.T) {
	first := newFake(&wsclient.CloseError{Code: CloseSuperseded}, `{"v":1,"type":"snapshot","online":["a"]}`)
	second := newFake(nil, `{"v":1,"type":"snapshot","online":["a","b"]}`)
	h := newHarness(t, func() (Conn, error) { return first, nil }, func() (Conn, error) { return second, nil })
	run(t, h, func() bool {
		s := h.c.Snapshot()
		return s.Connected && reflect.DeepEqual(s.Online, []string{"a", "b"})
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.c.Snapshot().Removed || h.dials < 2 {
		t.Fatalf("4002 treated as removal (dials=%d)", h.dials)
	}
	if len(h.sleeps) < 1 || h.sleeps[0] < MinBackoff/2 || h.sleeps[0] > MinBackoff {
		t.Fatalf("4002 did not wait the normal backoff: %v", h.sleeps)
	}
}

func TestBackoffAndUnsupported(t *testing.T) {
	fail := func() (Conn, error) { return nil, errors.New("dial failed") }
	unsupported := func() (Conn, error) { return nil, &client.StatusError{Status: 404} }
	h := newHarness(t, fail, fail, fail, unsupported)
	run(t, h, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.sleeps) >= 4 })
	h.mu.Lock()
	defer h.mu.Unlock()
	steps := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for i, step := range steps {
		if d := h.sleeps[i]; d < step/2 || d > step {
			t.Fatalf("sleep %d = %v, want within [%v,%v]", i, d, step/2, step)
		}
	}
	if h.sleeps[3] != UnsupportedBackoff {
		t.Fatalf("unsupported coordinator slept %v, want %v", h.sleeps[3], UnsupportedBackoff)
	}
	if got := h.c.Snapshot().LastError; got == "" {
		t.Fatal("no reason while disconnected")
	}
}

func TestBackoffBounds(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		d := backoff(attempt)
		if d < MinBackoff/2 || d > MaxBackoff {
			t.Fatalf("backoff(%d) = %v out of bounds", attempt, d)
		}
	}
	if backoff(30) < MaxBackoff/2 {
		t.Fatal("backoff did not reach the cap")
	}
}

func TestWakeRelay(t *testing.T) {
	conn := newFake(nil,
		`{"v":1,"type":"snapshot","online":[]}`,
		`{"v":1,"type":"wake.request","requestId":"r1","targetHostId":"other","macs":["3C-22-FB-01-02-03"],"at":"x"}`,
		`{"v":1,"type":"wake.request","requestId":"r1","targetHostId":"other","macs":["3c:22:fb:01:02:03"]}`, // duplicate
		`{"v":1,"type":"wake.request","requestId":"r2","targetHostId":"self","macs":["3c:22:fb:01:02:03"]}`,  // self
		`{"v":1,"type":"wake.request","requestId":"r3","targetHostId":"other","macs":["ff:ff:ff:ff:ff:ff"]}`, // broadcast MAC
		`{"v":1,"type":"wake.request","requestId":"r4","targetHostId":"other","macs":[]}`,
		`{"v":1,"type":"wake.request","requestId":"r5","targetHostId":"other","macs":["3c:22:fb:01:02:04"]}`, // rate limited (same instant)
		`{"v":1,"type":"host.online","hostId":"done"}`,
	)
	h := newHarness(t, func() (Conn, error) { return conn, nil })
	run(t, h, func() bool { return reflect.DeepEqual(h.c.Snapshot().Online, []string{"done"}) })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.wakes) != 1 || !reflect.DeepEqual(h.wakes[0].macs, []string{"3c:22:fb:01:02:03"}) {
		t.Fatalf("wakes = %+v", h.wakes)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{HostID: "", Dial: func(context.Context) (Conn, error) { return nil, nil }}); err == nil {
		t.Fatal("empty host id accepted")
	}
	if _, err := New(Options{HostID: "h"}); err == nil {
		t.Fatal("nil dialer accepted")
	}
}
