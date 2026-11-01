package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// pollCoord is a minimal Coordinator that counts task fetches and returns
// canned tasks or an error.
type pollCoord struct {
	mu    sync.Mutex
	calls int
	tasks []Task
	err   error
}

func (c *pollCoord) SandboxTasks(context.Context, string) ([]Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.tasks, c.err
}
func (c *pollCoord) ReportSandboxState(context.Context, string, StateReport) error { return nil }

func (c *pollCoord) fetches() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestPollRequiresBoundCoordinator(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, &fakeDev{}, &fakeGuest{})
	m.mu.Lock()
	m.coord = nil
	m.mu.Unlock()
	if err := m.Poll(context.Background(), "h1"); err == nil {
		t.Fatal("Poll without a coordinator must fail")
	}
}

func TestPollOptedOutAndIdleStaysOffTheNetwork(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, &fakeDev{}, &fakeGuest{})
	pc := &pollCoord{}
	m.mu.Lock()
	m.coord = pc
	m.caps.Enabled = false
	m.mu.Unlock()
	if err := m.Poll(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	if n := pc.fetches(); n != 0 {
		t.Fatalf("opted-out idle host fetched tasks %d times", n)
	}
}

func TestPollPropagatesFetchErrorAndRecordsHost(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, &fakeDev{}, &fakeGuest{})
	boom := errors.New("boom")
	pc := &pollCoord{err: boom}
	m.mu.Lock()
	m.coord = pc
	m.mu.Unlock()
	if err := m.Poll(context.Background(), "h1"); !errors.Is(err, boom) {
		t.Fatalf("Poll error = %v, want %v", err, boom)
	}
	m.mu.Lock()
	host := m.hostID
	m.mu.Unlock()
	if host != "h1" {
		t.Fatalf("hostID = %q, want h1", host)
	}
}

func TestPollDropsInvalidTasks(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, &fakeDev{}, &fakeGuest{})
	pc := &pollCoord{tasks: []Task{{TaskID: "t1", Kind: "bogus"}}}
	m.mu.Lock()
	m.coord = pc
	m.mu.Unlock()
	if err := m.Poll(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	if got := len(m.List()); got != 0 {
		t.Fatalf("invalid task created %d sandboxes", got)
	}
}
