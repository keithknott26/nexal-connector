package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type hostingCoord struct {
	mu   sync.Mutex
	puts []HostingConfig
	err  error
}

func (c *hostingCoord) SandboxTasks(context.Context, string) ([]Task, error) { return nil, nil }
func (c *hostingCoord) ReportSandboxState(context.Context, string, StateReport) error {
	return nil
}
func (c *hostingCoord) PutSandboxHosting(_ context.Context, _ string, h HostingConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts = append(c.puts, h)
	return c.err
}

func TestRefreshConfigPublishesOptInAndRetriesAfterFailure(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	path := filepath.Join(t.TempDir(), "sandbox-hosting.json")
	m.cfgw = &configWatcher{path: path}
	coord := &hostingCoord{err: errors.New("down")}
	m.coord = coord
	now := time.Unix(1000, 0)
	m.opts.Now = func() time.Time { return now }
	m.opts.Host = factsHost{err: errors.New("no facts")}
	ctx := context.Background()

	// Never opted in: nothing is published.
	m.refreshConfig(ctx, "h1")
	if len(coord.puts) != 0 {
		t.Fatalf("missing file must not publish, got %d", len(coord.puts))
	}

	if err := os.WriteFile(path, []byte(`{"enabled":true,"maxSandboxes":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.refreshConfig(ctx, "h1")
	if len(coord.puts) != 1 || coord.puts[0].MaxSandboxes != 3 || !coord.puts[0].Enabled {
		t.Fatalf("expected one publish of the file's config, got %+v", coord.puts)
	}
	if !m.Caps().Enabled || m.Caps().MaxSandboxes != 3 {
		t.Fatalf("caps not applied: %+v", m.Caps())
	}

	// A failed publish backs off for a minute, then retries.
	m.refreshConfig(ctx, "h1")
	if len(coord.puts) != 1 {
		t.Fatalf("must back off after failure, got %d puts", len(coord.puts))
	}
	now = now.Add(time.Minute)
	coord.mu.Lock()
	coord.err = nil
	coord.mu.Unlock()
	m.refreshConfig(ctx, "h1")
	if len(coord.puts) != 2 {
		t.Fatalf("must retry after backoff, got %d puts", len(coord.puts))
	}

	// Published: no further PUTs until the file changes.
	m.refreshConfig(ctx, "h1")
	if len(coord.puts) != 2 {
		t.Fatalf("published config must not be resent, got %d puts", len(coord.puts))
	}
}

func TestRefreshConfigWithoutWatcherIsNoop(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	m.refreshConfig(context.Background(), "h1")
}
