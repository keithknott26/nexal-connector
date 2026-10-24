package sandbox

import (
	"context"
	"errors"
	"testing"
)

type factsHost struct {
	f   HostFacts
	err error
}

func (h factsHost) Facts(context.Context, string) (HostFacts, error) { return h.f, h.err }

func TestManagerDiskCheck(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)

	m.opts.Host = factsHost{err: errors.New("df failed")}
	if err := m.diskCheck(context.Background(), 1<<30); err == nil || err.Error() != "df failed" {
		t.Fatalf("host error must pass through, got %v", err)
	}

	m.opts.Host = factsHost{f: HostFacts{FreeDisk: 1 << 30}}
	if err := m.diskCheck(context.Background(), 1<<30); err == nil {
		t.Fatal("no room for the image plus the safety margin must be refused")
	}

	m.opts.Host = factsHost{f: HostFacts{FreeDisk: 1 << 40}}
	if err := m.diskCheck(context.Background(), 1<<30); err != nil {
		t.Fatalf("plenty of disk must pass, got %v", err)
	}
}

func TestResumeAppWatchIgnoresNonRunningAndMissing(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	m.resumeAppWatch("nope") // unknown id: no panic, nothing started
	put(m, &record{ID: "stopped", State: StateStopped})
	m.resumeAppWatch("stopped")
	put(m, &record{ID: "noconsole", State: StateRunning})
	m.resumeAppWatch("noconsole") // no console log yet: nothing to resume
}
