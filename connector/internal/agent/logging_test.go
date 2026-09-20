package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

// syncBuffer serializes handler writes against the test's reads. slog handlers
// already lock their writer, but the test goroutine is not one of those writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// records decodes the JSON log lines, failing if any line is not a JSON object:
// an operator's log pipeline has to be able to parse every line.
func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func (b *syncBuffer) find(t *testing.T, msg string) map[string]any {
	t.Helper()
	for _, r := range b.records(t) {
		if r["msg"] == msg {
			return r
		}
	}
	t.Fatalf("no %q record in:\n%s", msg, b.String())
	return nil
}

type failingHeartbeatAPI struct{ fakeAPI }

func (f *failingHeartbeatAPI) Heartbeat(context.Context, string, client.Heartbeat) error {
	return errors.New("coordinator rejected request (HTTP 503)")
}

func loggedAgent(t *testing.T, api client.API) (*Agent, *syncBuffer) {
	t.Helper()
	c := config.Config{Version: 1, Coordinator: "http://127.0.0.1:8787", Name: "test", HostID: "h1",
		Listen: "127.0.0.1:8788", Development: true, DevSecrets: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	probe := func(context.Context) Telemetry {
		return Telemetry{Known: true, Synthetic: true, AvailableMemoryBytes: 1 << 30,
			TotalMemoryBytes: 2 << 30, IdleSeconds: 1000}
	}
	logs := &syncBuffer{}
	a, err := New(c, path, api, probe, true,
		WithLogger(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err != nil {
		t.Fatal(err)
	}
	a.Refresh(context.Background())
	a.lastHeartbeat = time.Now() // admission requires a fresh coordinator heartbeat
	return a, logs
}

// A failing heartbeat clears lastHeartbeat and cancels running work, and used to
// be discarded with `_ =`, so an operator saw an attempt die for no stated reason.
func TestHeartbeatFailureIsLoggedWithTrigger(t *testing.T) {
	a, logs := loggedAgent(t, &failingHeartbeatAPI{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.After(5 * time.Second)
	for {
		if strings.Contains(logs.String(), "host heartbeat failed") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no heartbeat failure logged:\n%s", logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	r := logs.find(t, "host heartbeat failed")
	if r["level"] != "WARN" {
		t.Fatalf("heartbeat failure logged at %v", r["level"])
	}
	if r["trigger"] != "startup" {
		t.Fatalf("trigger not recorded: %+v", r)
	}
	if r["error"] != "coordinator rejected request (HTTP 503)" {
		t.Fatalf("error text not recorded: %+v", r)
	}
}

// Attempt lifecycle records must exist and must name the attempt, and no record
// may carry a credential. The admin token and host credential are the two secrets
// in reach of this package.
func TestAttemptLifecycleIsLoggedWithoutSecrets(t *testing.T) {
	a, logs := loggedAgent(t, &fakeAPI{})
	at := attempt()
	if err := a.Execute(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	settled := logs.find(t, "attempt settled")
	if settled["attempt"] != at.ID || settled["state"] != "completed" {
		t.Fatalf("settlement not described: %+v", settled)
	}
	if _, ok := settled["seconds"]; !ok {
		t.Fatalf("no duration recorded: %+v", settled)
	}
	// Every field of every record is inspected, not just the ones asserted above.
	for _, r := range logs.records(t) {
		for k, v := range r {
			text, ok := v.(string)
			if !ok {
				continue
			}
			for _, secret := range []string{"host-scoped-secret", "Bearer ", "http://127.0.0.1:8787"} {
				if strings.Contains(text, secret) {
					t.Fatalf("log field %q leaks %q: %+v", k, secret, r)
				}
			}
		}
	}
}

// An abandoned attempt must say which of the four reasons applied, and a restart
// must report the attempts it fenced.
func TestAbandonmentAndFencingAreLogged(t *testing.T) {
	a, logs := loggedAgent(t, &fakeAPI{})
	at := attempt()
	// Same shape as TestLeaseExpiresDuringWork: long work, lease already going.
	at.Samples = MaxSamples
	at.LeaseExpiresAt = time.Now().Add(2 * time.Millisecond)
	if err := a.Execute(context.Background(), at); err == nil {
		t.Fatal("expired lease reported success")
	}
	r := logs.find(t, "attempt abandoned")
	if r["attempt"] != at.ID {
		t.Fatalf("abandonment does not name the attempt: %+v", r)
	}
	for _, field := range []string{"workload_error", "context_error", "admitted", "lease_expired"} {
		if _, ok := r[field]; !ok {
			t.Fatalf("abandonment reason %q missing: %+v", field, r)
		}
	}

	// Reopen against the same journal with a running record to exercise fencing.
	a.mu.Lock()
	a.records["a2"] = Record{Fingerprint: "f", State: "running", LeaseExpiresAt: time.Now().Add(time.Minute)}
	err := a.persistLocked()
	path := a.path
	cfg := a.cfg
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	restart := &syncBuffer{}
	probe := func(context.Context) Telemetry { return Telemetry{Known: true, Synthetic: true, IdleSeconds: 1000} }
	if _, err := New(cfg, path, &fakeAPI{}, probe, true,
		WithLogger(slog.New(slog.NewJSONHandler(restart, nil)))); err != nil {
		t.Fatal(err)
	}
	if fenced := restart.find(t, "fenced attempt interrupted by restart"); fenced["attempt"] != "a2" {
		t.Fatalf("fenced attempt not named: %+v", fenced)
	}
}

// Without WithLogger an Agent must stay silent: the CLI opts in, libraries and
// tests do not inherit output on stderr.
func TestLoggingIsOffByDefault(t *testing.T) {
	a, _ := testAgent(t)
	if a.logger == nil {
		t.Fatal("nil logger would panic at every log site")
	}
	if a.logger.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("default logger is not the discard handler")
	}
}
