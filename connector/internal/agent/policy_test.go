package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"nexal/connector/internal/config"
)

func TestPolicyPersistsCancelsAndRequiresFreshTelemetry(t *testing.T) {
	a, _ := testAgent(t)
	cancelled := false
	a.cancel = func() { cancelled = true }
	// UploadMode is stated explicitly: SetResourcePolicy normalizes an omitted
	// mode to auto, so a persisted policy always carries the resolved value.
	p := config.ResourcePolicy{MemoryLimitBytes: 128 << 20, ReserveMemoryBytes: 256 << 20,
		IdleSeconds: 2000, UploadMode: config.UploadModeAuto}
	if err := a.SetResourcePolicy(p); err != nil {
		t.Fatal(err)
	}
	if !cancelled || a.Snapshot().Telemetry.Known || !a.Snapshot().Telemetry.OwnerActive {
		t.Fatal("policy change did not fence old telemetry and stop work")
	}
	c, err := config.Load(a.path)
	if err != nil || c.ResourcePolicy() != p || c.HostID != "h1" || c.MarketplaceEnabled {
		t.Fatal("policy persistence or identity boundary violated")
	}
	a.Refresh(context.Background())
	if !a.Snapshot().Telemetry.OwnerActive {
		t.Fatal("new idle threshold was not applied")
	}
	p.IdleSeconds = 30
	if err = a.SetResourcePolicy(p); err != nil {
		t.Fatal(err)
	}
	a.Refresh(context.Background())
	if a.Snapshot().Telemetry.OwnerActive {
		t.Fatal("relaxed idle threshold ignored")
	}
}

func TestPolicyFailurePausesWithoutApplyingUnsavedLimits(t *testing.T) {
	a, _ := testAgent(t)
	old := a.Snapshot().ResourcePolicy
	if err := os.Chmod(filepath.Dir(a.path), 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Dir(a.path), 0700)
	next := old
	next.IdleSeconds++
	if err := a.SetResourcePolicy(next); err == nil {
		t.Fatal("unsafe persistence accepted")
	}
	if s := a.Snapshot(); !s.Paused || s.ResourcePolicy != old || s.Telemetry.Known {
		t.Fatal("persistence failure was not fail-closed")
	}
}

func TestInvalidAndIdenticalPolicyDoNotAlterConsent(t *testing.T) {
	a, _ := testAgent(t)
	old := a.Snapshot()
	cancelled := false
	a.cancel = func() { cancelled = true }
	if err := a.SetResourcePolicy(config.ResourcePolicy{}); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if err := a.SetResourcePolicy(old.ResourcePolicy); err != nil {
		t.Fatal(err)
	}
	if cancelled || a.Snapshot().ResourcePolicy != old.ResourcePolicy || !a.Snapshot().Telemetry.Known {
		t.Fatal("invalid or idempotent update changed running state")
	}
}

func TestPolicyLocalAPISecurity(t *testing.T) {
	a, _ := testAgent(t)
	token := strings.Repeat("x", 64)
	handler, err := a.Handler(token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(a.Snapshot().ResourcePolicy)
	for _, tc := range []struct {
		method, body, contentType, origin, auth string
		want                                    int
	}{
		{"GET", "", "", "", token, 200},
		{"PUT", string(body), "application/json", "", token, 200},
		{"PUT", string(body), "text/plain", "", token, 415},
		{"PUT", string(body), "application/json", "https://evil.test", token, 403},
		{"PUT", string(body), "application/json", "", "", 401},
		{"PUT", `{}`, "application/json", "", token, 400},
		{"PUT", strings.Repeat("x", 4097), "application/json", "", token, 413},
		{"PATCH", string(body), "application/json", "", token, 405},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:8788/v1/policy", strings.NewReader(tc.body))
		r.RemoteAddr = "127.0.0.1:54321"
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s: got %d want %d", tc.method, w.Code, tc.want)
		}
	}
}

func TestConcurrentPolicyPauseAndSnapshot(t *testing.T) {
	a, _ := testAgent(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := config.ResourcePolicy{MemoryLimitBytes: 128 << 20, ReserveMemoryBytes: 256 << 20,
				IdleSeconds: uint64(300 + i), UploadMode: config.UploadModeAuto}
			if err := a.SetResourcePolicy(p); err != nil {
				t.Error(err)
			}
			if err := a.SetPaused(true); err != nil {
				t.Error(err)
			}
			a.Refresh(context.Background())
			_ = a.Snapshot()
		}(i)
	}
	wg.Wait()
	c, err := config.Load(a.path)
	if err != nil || !c.Paused || c.ResourcePolicy() != a.Snapshot().ResourcePolicy {
		t.Fatal("concurrent updates lost persisted consent")
	}
}
