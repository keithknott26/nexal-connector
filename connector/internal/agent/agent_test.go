package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

type fakeAPI struct {
	mu          sync.Mutex
	completed   int
	renewCancel bool
}

func (f *fakeAPI) Heartbeat(context.Context, string, client.Heartbeat) (client.HeartbeatResponse, error) {
	return client.HeartbeatResponse{}, nil
}
func (f *fakeAPI) Next(context.Context, string) (*client.Attempt, error)     { return nil, nil }
func (f *fakeAPI) SelfTest(context.Context, string) (bool, *client.Attempt, error) {
	return false, nil, nil
}
func (f *fakeAPI) Renew(context.Context, string) (client.Renewal, error) {
	return client.Renewal{OK: true, CancelRequested: f.renewCancel, LeaseExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (f *fakeAPI) Complete(_ context.Context, _ string, r client.Result, _ float64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed++
	return true, nil
}
func (f *fakeAPI) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.completed }
func testAgent(t *testing.T) (*Agent, *fakeAPI) {
	t.Helper()
	c := config.Config{Version: 1, Coordinator: "http://127.0.0.1:8787", Name: "test", HostID: "h1", Listen: "127.0.0.1:8788",
		Development: true, DevSecrets: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{}
	probe := func(context.Context) Telemetry {
		return Telemetry{Known: true, Synthetic: true, AvailableMemoryBytes: 1 << 30, TotalMemoryBytes: 2 << 30, IdleSeconds: 1000}
	}
	a, err := New(c, path, f, probe, true)
	if err != nil {
		t.Fatal(err)
	}
	a.Refresh(context.Background())
	a.lastHeartbeat = time.Now()
	return a, f
}
func attempt() client.Attempt {
	return client.Attempt{ID: "a1", JobID: "j1", HostID: "h1", Template: Template, Samples: 10000, LeaseExpiresAt: time.Now().Add(time.Minute)}
}
func TestIdempotentDuplicateAndCrashJournal(t *testing.T) {
	a, f := testAgent(t)
	at := attempt()
	if err := a.Execute(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if err := a.Execute(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatal("duplicate computation/completion")
	}
	b, err := New(a.cfg, a.path, f, a.probe, true)
	if err != nil {
		t.Fatal(err)
	}
	b.Refresh(context.Background())
	b.lastHeartbeat = time.Now()
	if err := b.Execute(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatal("restarted duplicate executed")
	}
	at.Samples++
	if err := b.Execute(context.Background(), at); err == nil {
		t.Fatal("conflicting replay accepted")
	}
}
func TestRejectStaleWrongHostUnknownTemplate(t *testing.T) {
	for name, modify := range map[string]func(*client.Attempt){
		"stale":            func(a *client.Attempt) { a.LeaseExpiresAt = time.Now().Add(-time.Second) },
		"overlong":         func(a *client.Attempt) { a.LeaseExpiresAt = time.Now().Add(time.Hour) },
		"wrong-host":       func(a *client.Attempt) { a.HostID = "other" },
		"unknown-template": func(a *client.Attempt) { a.Template = "shell" },
		"unbounded":        func(a *client.Attempt) { a.Samples = MaxSamples + 1 },
		"traversal":        func(a *client.Attempt) { a.ID = "../x" },
		"negative-cost":    func(a *client.Attempt) { a.MaxCostCents = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			a, f := testAgent(t)
			at := attempt()
			modify(&at)
			if a.Execute(context.Background(), at) == nil {
				t.Fatal("unsafe attempt accepted")
			}
			if f.count() != 0 {
				t.Fatal("unsafe attempt completed")
			}
		})
	}
}
func TestResourceAdmissionOwnerPriority(t *testing.T) {
	for name, modify := range map[string]func(*Agent){
		"paused":            func(a *Agent) { a.cfg.Paused = true },
		"owner":             func(a *Agent) { a.telemetry.OwnerActive = true },
		"unknown":           func(a *Agent) { a.telemetry.Known = false },
		"stale-telemetry":   func(a *Agent) { a.telemetryAt = time.Now().Add(-time.Minute) },
		"stale-coordinator": func(a *Agent) { a.lastHeartbeat = time.Now().Add(-time.Minute) },
		"low-memory":        func(a *Agent) { a.telemetry.AvailableMemoryBytes = 1 },
		"invalid-memory":    func(a *Agent) { a.telemetry.AvailableMemoryBytes = 3 << 30 },
		"private-limit":     func(a *Agent) { a.cfg.MemoryLimitBytes = 1 },
		"busy":              func(a *Agent) { a.active = "other" },
		"production":        func(a *Agent) { a.cfg.Development = false },
		"pull-disabled":     func(a *Agent) { a.devPull = false },
	} {
		t.Run(name, func(t *testing.T) {
			a, f := testAgent(t)
			modify(a)
			if a.Execute(context.Background(), attempt()) == nil {
				t.Fatal("resource gate bypassed")
			}
			if f.count() != 0 {
				t.Fatal("inadmissible work completed")
			}
		})
	}
}
func TestLeaseExpiresDuringWork(t *testing.T) {
	a, f := testAgent(t)
	at := attempt()
	at.Samples = MaxSamples
	at.LeaseExpiresAt = time.Now().Add(2 * time.Millisecond)
	if err := a.Execute(context.Background(), at); err == nil {
		t.Fatal("lease expiry ignored")
	}
	if f.count() != 0 {
		t.Fatal("stale completion sent")
	}
}
func TestPauseCancelsActiveAndPersists(t *testing.T) {
	a, f := testAgent(t)
	at := attempt()
	at.Samples = MaxSamples
	done := make(chan error, 1)
	go func() { done <- a.Execute(context.Background(), at) }()
	for n := 0; n < 100 && a.Snapshot().ActiveAttempt == ""; n++ {
		time.Sleep(time.Millisecond)
	}
	if a.Snapshot().ActiveAttempt == "" {
		t.Fatal("work did not start")
	}
	if err := a.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pause did not cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation timeout")
	}
	if f.count() != 0 {
		t.Fatal("cancelled work completed")
	}
	c, err := config.Load(a.path)
	if err != nil || !c.Paused {
		t.Fatal("pause not persisted")
	}
}
func TestOwnerTelemetryMissingCancels(t *testing.T) {
	a, _ := testAgent(t)
	cancelled := false
	a.cancel = func() { cancelled = true }
	a.probe = func(context.Context) Telemetry { return Telemetry{} }
	a.Refresh(context.Background())
	if !cancelled || !a.Snapshot().Telemetry.OwnerActive {
		t.Fatal("unknown telemetry did not fail closed")
	}
}
func TestLocalAPIAuthBoundsAndInvalidGrant(t *testing.T) {
	a, _ := testAgent(t)
	token := strings.Repeat("x", 64)
	h, err := a.Handler(token)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, method, body, auth, origin string
		code                             int
	}{
		{"/v1/status", "GET", "", "", "", 401},
		{"/v1/status", "GET", "", token, "https://evil.test", 403},
		{"/v1/status", "GET", "", token, "", 200},
		{"/v1/attempts", "POST", `{"grant":"invalid","template":"shell"}`, token, "", 403},
		{"/v1/pause", "POST", strings.Repeat("a", 4097), token, "", 413},
		{"/v1/pause", "POST", `{"run":"shell"}`, token, "", 400},
		{"/v1/pause", "POST", "{}", token, "", 200},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://127.0.0.1:8788"+c.path, strings.NewReader(c.body))
		r.RemoteAddr = "127.0.0.1:12345"
		if c.auth != "" {
			r.Header.Set("Authorization", "Bearer "+c.auth)
		}
		r.Header.Set("Origin", c.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Errorf("%s got %d want %d", c.path, w.Code, c.code)
		}
	}
	if a.Snapshot().ActiveAttempt != "" {
		t.Fatal("invalid grant started job")
	}
}
func TestProductionGateEvenWithReportedPQ(t *testing.T) {
	a, _ := testAgent(t)
	a.SetPQ(client.PQ{Configured: true, Verified: true, Protocol: "quic"})
	if a.Snapshot().PQ.Verified {
		t.Fatal("self-report became attestation")
	}
	a.cfg.Development = false
	a.devPull = false
	if err := a.Execute(context.Background(), attempt()); err == nil {
		t.Fatal("production gate bypassed")
	}
	if _, err := New(a.cfg, a.path, a.api, a.probe, true); err == nil {
		t.Fatal("production pull bypassed")
	}
}
func TestMonteCarloDeterministicConstantWorkload(t *testing.T) {
	at := attempt()
	at.Samples = 100000
	deadline := func() time.Time { return time.Now().Add(time.Minute) }
	r, err := MonteCarlo(context.Background(), at, deadline)
	if err != nil || r.Pi < 3 || r.Pi > 3.3 || r.Samples != at.Samples {
		t.Fatalf("%+v %v", r, err)
	}
	r2, _ := MonteCarlo(context.Background(), at, deadline)
	b1, _ := json.Marshal(r)
	b2, _ := json.Marshal(r2)
	if string(b1) != string(b2) {
		t.Fatal("workload nondeterministic")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MonteCarlo(ctx, at, deadline); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestAPIRejectsNonloopbackEvenAuthenticated(t *testing.T) {
	a, _ := testAgent(t)
	token := strings.Repeat("x", 64)
	h, _ := a.Handler(token)
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/v1/status", nil)
	r.RemoteAddr = "192.0.2.1:5000"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("remote API accepted")
	}
}
