package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/presence"
	"nexal/connector/internal/wol"
)

type fakePresence struct{ snap presence.Snapshot }

func (f fakePresence) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func (f fakePresence) Snapshot() presence.Snapshot   { return f.snap }

// TestStatusPresenceAndWakeShape pins the additive /v1/status contract: both
// objects are always present, arrays are never null, and the existing fields
// are untouched (spot-checked by name).
func TestStatusPresenceAndWakeShape(t *testing.T) {
	a, _ := testAgent(t)
	b, _ := json.Marshal(a.Snapshot())
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"hostId", "paused", "telemetry", "resourcePolicy", "uploadThrottle", "contribution", "mesh", "pq", "coordinatorHealthy"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("existing status field %q disappeared", key)
		}
	}
	if string(m["presence"]) != `{"connected":false,"online":[],"updatedAt":"","detail":"live presence is not running"}` {
		t.Fatalf("presence without a stream = %s", m["presence"])
	}
	if string(m["wake"]) != `{"macs":[],"wakeForNetwork":"unknown","reported":false}` {
		t.Fatalf("wake before collection = %s", m["wake"])
	}

	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	a.presence = fakePresence{presence.Snapshot{Connected: true, Online: []string{"a", "b"}, UpdatedAt: at}}
	got := a.Snapshot().Presence
	want := PresenceStatus{Connected: true, Online: []string{"a", "b"}, UpdatedAt: "2026-09-24T12:00:00.000Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("connected presence = %+v", got)
	}
	a.presence = fakePresence{presence.Snapshot{Online: []string{"a"}, UpdatedAt: at, LastError: "cannot reach the coordinator"}}
	got = a.Snapshot().Presence
	if got.Connected || got.Detail != "cannot reach the coordinator" || !reflect.DeepEqual(got.Online, []string{"a"}) {
		t.Fatalf("disconnected presence = %+v", got)
	}
}

type fakeWakeReporter struct {
	mu    sync.Mutex
	calls []client.WakeInfo
	err   error
}

func (f *fakeWakeReporter) ReportWakeInfo(_ context.Context, w client.WakeInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, w)
	return f.err
}
func (f *fakeWakeReporter) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func TestWakeInfoReportsOnlyOnChange(t *testing.T) {
	a, _ := testAgent(t)
	rep := &fakeWakeReporter{}
	a.wakeReporter = rep
	a.wakeInfoEvery = 5 * time.Millisecond
	key1 := "1111111111111111111111111111111111111111111111111111111111111111"
	key2 := "2222222222222222222222222222222222222222222222222222222222222222"
	var mu sync.Mutex
	facts := wol.Facts{MACs: []string{"3c:22:fb:01:02:03"}, LANKey: key1, WakeForNetwork: wol.WakeEnabled}
	a.wakeFacts = func(context.Context) wol.Facts {
		mu.Lock()
		defer mu.Unlock()
		return facts
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.runWakeInfo(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "first wake report", func() bool { return a.Snapshot().Wake.Reported })
	time.Sleep(50 * time.Millisecond) // several unchanged ticks
	if rep.n() != 1 {
		t.Fatalf("unchanged facts reported %d times", rep.n())
	}
	s := a.Snapshot().Wake
	if !reflect.DeepEqual(s, WakeStatus{MACs: []string{"3c:22:fb:01:02:03"}, WakeForNetwork: "enabled", Reported: true}) {
		t.Fatalf("wake status = %+v", s)
	}
	mu.Lock()
	facts.LANKey = key2
	mu.Unlock()
	waitFor(t, "changed wake report", func() bool { return rep.n() == 2 })
	rep.mu.Lock()
	last := rep.calls[1]
	rep.mu.Unlock()
	if last.LANKey != key2 || !last.WakeForNetwork {
		t.Fatalf("changed facts not reported: %+v", last)
	}
}

func TestWakeInfoUnsupportedAndFailure(t *testing.T) {
	a, _ := testAgent(t)
	rep := &fakeWakeReporter{err: &client.StatusError{Status: 404}}
	a.wakeReporter = rep
	a.wakeInfoEvery = 5 * time.Millisecond
	a.wakeFacts = func(context.Context) wol.Facts {
		return wol.Facts{MACs: []string{"3c:22:fb:01:02:03"},
			LANKey: "1111111111111111111111111111111111111111111111111111111111111111", WakeForNetwork: wol.WakeDisabled}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.runWakeInfo(ctx); close(done) }()
	waitFor(t, "wake report retry", func() bool { return rep.n() >= 2 }) // retried later, quietly
	cancel()
	<-done
	s := a.Snapshot().Wake
	if s.Reported || s.WakeForNetwork != "disabled" || len(s.MACs) != 1 {
		t.Fatalf("wake status after unsupported = %+v", s)
	}
	// A Mac with no usable network yet has nothing to report and must not send
	// an invalid body.
	a2, _ := testAgent(t)
	rep2 := &fakeWakeReporter{err: errors.New("unused")}
	a2.wakeReporter = rep2
	a2.wakeInfoEvery = 5 * time.Millisecond
	a2.wakeFacts = func(context.Context) wol.Facts {
		return wol.Facts{MACs: []string{"3c:22:fb:01:02:03"}, WakeForNetwork: wol.WakeUnknown}
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { a2.runWakeInfo(ctx2); close(done2) }()
	time.Sleep(30 * time.Millisecond)
	cancel2()
	<-done2
	if rep2.n() != 0 {
		t.Fatal("reported wake info without a lanKey")
	}
}
