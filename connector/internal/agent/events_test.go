package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/wsclient"
)

type fakeMsg struct {
	data string
	err  error
}

// fakeStream is a scripted event socket.
type fakeStream struct {
	reads     chan fakeMsg
	mu        sync.Mutex
	writes    []string
	closeCode int
	closed    chan struct{}
	once      sync.Once
}

func newFakeStream() *fakeStream {
	return &fakeStream{reads: make(chan fakeMsg, 16), closed: make(chan struct{})}
}

func (f *fakeStream) ReadMessage() (wsclient.MessageType, []byte, error) {
	select {
	case m := <-f.reads:
		if m.err != nil {
			return 0, nil, m.err
		}
		return wsclient.TextMessage, []byte(m.data), nil
	case <-f.closed:
		return 0, nil, errors.New("use of closed connection")
	}
}
func (f *fakeStream) WriteText(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, s)
	return nil
}
func (f *fakeStream) SetReadDeadline(time.Time) error { return nil }
func (f *fakeStream) Close(code int) error {
	f.once.Do(func() {
		f.mu.Lock()
		f.closeCode = code
		f.mu.Unlock()
		close(f.closed)
	})
	return nil
}
func (f *fakeStream) pings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, w := range f.writes {
		if w == "ping" {
			n++
		}
	}
	return n
}

func quietAgent() *Agent {
	return &Agent{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func fastEvents(t *testing.T) {
	t.Helper()
	prev := []time.Duration{eventsPingInterval, eventsBackoffMin, eventsBackoffMax, eventsStableAfter}
	eventsPingInterval, eventsBackoffMin, eventsBackoffMax, eventsStableAfter = 5*time.Millisecond, time.Millisecond, 4*time.Millisecond, time.Hour
	t.Cleanup(func() {
		eventsPingInterval, eventsBackoffMin, eventsBackoffMax, eventsStableAfter = prev[0], prev[1], prev[2], prev[3]
	})
}

func TestPresenceEvents(t *testing.T) {
	a := quietAgent()
	a.handleEvent([]byte(`{"v":1,"type":"snapshot","online":["h3","h1","bad id",""],"at":"2026-09-24T00:00:00Z"}`), "self")
	if got := a.OnlineHosts(); !reflect.DeepEqual(got, []string{"h1", "h3"}) {
		t.Fatal(got)
	}
	a.handleEvent([]byte(`{"v":1,"type":"host.online","hostId":"h2","at":"x"}`), "self")
	a.handleEvent([]byte(`{"v":1,"type":"host.offline","hostId":"h1","at":"x"}`), "self")
	a.handleEvent([]byte(`{"v":1,"type":"host.removed","hostId":"h3","at":"x"}`), "self")
	a.handleEvent([]byte(`{"v":1,"type":"future.thing","hostId":"h9","extra":{}}`), "self")
	a.handleEvent([]byte(`{"v":2,"type":"host.online","hostId":"h8"}`), "self")
	a.handleEvent([]byte(`pong`), "self")
	a.handleEvent([]byte(`{not json`), "self")
	a.handleEvent([]byte(`{"v":1,"type":"snapshot","online":"h1"}`), "self") // wrong shape ignored
	if got := a.OnlineHosts(); !reflect.DeepEqual(got, []string{"h2"}) {
		t.Fatal(got)
	}
	a.handleEvent([]byte(`{"v":1,"type":"snapshot","online":[]}`), "self")
	if got := a.OnlineHosts(); len(got) != 0 || got == nil {
		t.Fatalf("%#v", got)
	}
}

func TestWakeRequestEvent(t *testing.T) {
	prev := wakeBroadcast
	defer func() { wakeBroadcast = prev }()
	var sent [][]string
	wakeBroadcast = func(macs []string) error { sent = append(sent, macs); return nil }
	a := quietAgent()
	a.handleEvent([]byte(`{"v":1,"type":"wake.request","requestId":"6f1c2a52-8a4b-4d5e-9c1f-0a1b2c3d4e5f","targetHostId":"h2","macs":["a4:83:e7:12:34:56","A4:83:E7:12:34:57","a4:83:e7:12:34:56","3c:22:fb:00:00:01"],"at":"x"}`), "self")
	if len(sent) != 1 || !reflect.DeepEqual(sent[0], []string{"a4:83:e7:12:34:56", "3c:22:fb:00:00:01"}) {
		t.Fatalf("%v", sent)
	}
	// This Mac is the target: nothing to do. No valid MAC: nothing sent.
	a.handleEvent([]byte(`{"v":1,"type":"wake.request","requestId":"r2","targetHostId":"self","macs":["a4:83:e7:12:34:56"]}`), "self")
	a.handleEvent([]byte(`{"v":1,"type":"wake.request","requestId":"r3","targetHostId":"h2","macs":["01:00:5e:00:00:01"]}`), "self")
	if len(sent) != 1 {
		t.Fatalf("%v", sent)
	}
	wakeBroadcast = func([]string) error { return errors.New("no route") }
	a.handleEvent([]byte(`{"v":1,"type":"wake.request","requestId":"r4","targetHostId":"h2","macs":["a4:83:e7:12:34:56"]}`), "self") // logged, no panic
}

func TestEventsLoopStopsOnTerminalCloseCodes(t *testing.T) {
	fastEvents(t)
	for _, code := range []int{4001, 4002} {
		a := quietAgent()
		dials := 0
		a.dialEvents = func(context.Context) (eventStream, error) {
			dials++
			s := newFakeStream()
			s.reads <- fakeMsg{data: `{"v":1,"type":"snapshot","online":["h1"]}`}
			s.reads <- fakeMsg{err: &wsclient.CloseError{Code: code}}
			return s, nil
		}
		done := make(chan struct{})
		go func() { a.runEvents(context.Background(), "self"); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("code %d: loop kept reconnecting", code)
		}
		if dials != 1 || len(a.OnlineHosts()) != 0 {
			t.Fatalf("code %d: dials %d presence %v", code, dials, a.OnlineHosts())
		}
	}
}

func TestEventsLoopReconnectsPingsAndShutsDown(t *testing.T) {
	fastEvents(t)
	a := quietAgent()
	var mu sync.Mutex
	var streams []*fakeStream
	dialErrs := 2
	a.dialEvents = func(context.Context) (eventStream, error) {
		mu.Lock()
		defer mu.Unlock()
		if dialErrs > 0 { // a redeploy: the first dials fail
			dialErrs--
			return nil, errors.New("coordinator request failed")
		}
		s := newFakeStream()
		if len(streams) == 0 {
			s.reads <- fakeMsg{data: `{"v":1,"type":"snapshot","online":["h1"]}`}
			go func() {
				// Let the pinger run, then drop the socket abnormally.
				for s.pings() < 2 {
					time.Sleep(time.Millisecond)
				}
				s.reads <- fakeMsg{err: &wsclient.CloseError{Code: wsclient.CloseAbnormal}}
			}()
		} else {
			s.reads <- fakeMsg{data: `{"v":1,"type":"snapshot","online":["h7"]}`}
		}
		streams = append(streams, s)
		return s, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.runEvents(ctx, "self"); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(streams)
		mu.Unlock()
		if n == 2 && reflect.DeepEqual(a.OnlineHosts(), []string{"h7"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("streams %d presence %v", n, a.OnlineHosts())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop on shutdown")
	}
	mu.Lock()
	defer mu.Unlock()
	select {
	case <-streams[1].closed:
	default:
		t.Fatal("socket not closed on shutdown")
	}
	if streams[1].closeCode != wsclient.CloseGoingAway {
		t.Fatalf("close code %d", streams[1].closeCode)
	}
	if len(a.OnlineHosts()) != 0 {
		t.Fatal("presence must be cleared while disconnected")
	}
}

func TestEventsLoopDisabledWithoutEventsAPI(t *testing.T) {
	a := quietAgent()
	a.api = &fakeAPI{}
	done := make(chan struct{})
	go func() { a.runEvents(context.Background(), "self"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runEvents did not return for an API without an event stream")
	}
}
