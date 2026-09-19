package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/client"
)

func manualAgent(t *testing.T) *Agent {
	t.Helper()
	a, _ := testAgent(t)
	a.devPull = false
	a.probe = func(context.Context) Telemetry {
		return Telemetry{Known: true, OwnerActive: true, TotalMemoryBytes: 2 << 30, AvailableMemoryBytes: 1 << 30}
	}
	a.Refresh(context.Background())
	if err := a.AcceptJobsNow(); err != nil {
		t.Fatal(err)
	}
	a.Refresh(context.Background())
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestManualAcceptanceRunsWhileOwnerActiveWithoutSyntheticTelemetry(t *testing.T) {
	a := manualAgent(t)
	s := a.Snapshot()
	if !s.OwnerActivityOverride || !s.Telemetry.OwnerActive || s.Telemetry.Synthetic || s.ExecutionBlocker != "" {
		t.Fatalf("incorrect consent or telemetry: %+v", s)
	}
	at := attempt()
	at.Execution = "private"
	if err := a.Execute(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().LastOutcome != "completed" {
		t.Fatal("not completed")
	}
}

func TestManualAcceptancePreservesSafetyGates(t *testing.T) {
	for name, modify := range map[string]func(*Agent){
		"memory":     func(a *Agent) { a.telemetry.AvailableMemoryBytes = 1 },
		"unknown":    func(a *Agent) { a.telemetry.Known = false },
		"stale":      func(a *Agent) { a.telemetryAt = time.Now().Add(-time.Minute) },
		"heartbeat":  func(a *Agent) { a.lastHeartbeat = time.Time{} },
		"paused":     func(a *Agent) { a.cfg.Paused = true },
		"production": func(a *Agent) { a.cfg.Development = false },
		"expired":    func(a *Agent) { a.manualUntil = time.Now().Add(-time.Second); a.telemetry.OwnerActive = false },
	} {
		t.Run(name, func(t *testing.T) {
			a := manualAgent(t)
			modify(a)
			at := attempt()
			at.Execution = "private"
			if a.Execute(context.Background(), at) == nil {
				t.Fatal("gate bypassed")
			}
		})
	}
}

func TestManualAcceptanceRejectsNonPrivateOrNonzeroCost(t *testing.T) {
	for _, execution := range []string{"", "marketplace", "managed", "private"} {
		t.Run(execution, func(t *testing.T) {
			a := manualAgent(t)
			at := attempt()
			at.Execution = execution
			if execution == "private" {
				at.MaxCostCents = 1
			}
			if a.Execute(context.Background(), at) == nil {
				t.Fatal("scope bypassed")
			}
		})
	}
}

func TestManualPermissionClearedByPauseCancelPolicyAndRestart(t *testing.T) {
	for _, action := range []string{"pause", "cancel", "policy", "restart"} {
		t.Run(action, func(t *testing.T) {
			a := manualAgent(t)
			switch action {
			case "pause":
				if err := a.SetPaused(true); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				a.Cancel()
			case "policy":
				p := a.cfg.ResourcePolicy()
				p.IdleSeconds++
				if err := a.SetResourcePolicy(p); err != nil {
					t.Fatal(err)
				}
			case "restart":
				var err error
				a, err = New(a.cfg, a.path, a.api, a.probe, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if a.Snapshot().OwnerActivityOverride || a.devPull {
				t.Fatal("manual authority survived")
			}
		})
	}
}

func TestManualPermissionRetriesDoNotExtendDeadline(t *testing.T) {
	a := manualAgent(t)
	until := a.manualUntil
	if err := a.AcceptJobsNow(); err != nil {
		t.Fatal(err)
	}
	if !a.manualUntil.Equal(until) {
		t.Fatal("retry extended permission")
	}
}

func TestManualPermissionProductionAndPersistenceFailureDoNotResume(t *testing.T) {
	for _, production := range []bool{false, true} {
		a, _ := testAgent(t)
		a.cfg.Paused = true
		a.devPull = false
		if production {
			a.cfg.Development = false
		} else {
			a.path = "/dev/null/config.json"
		}
		if a.AcceptJobsNow() == nil {
			t.Fatal("invalid consent accepted")
		}
		if !a.cfg.Paused || a.devPull || a.Snapshot().OwnerActivityOverride {
			t.Fatal("failure changed consent")
		}
	}
}

type manualHeartbeatAPI struct {
	fakeAPI
	last client.Heartbeat
}

func (f *manualHeartbeatAPI) Heartbeat(_ context.Context, _ string, h client.Heartbeat) error {
	f.last = h
	return nil
}

func TestManualHeartbeatPreservesOwnerActivityAndCarriesDeadline(t *testing.T) {
	a := manualAgent(t)
	f := &manualHeartbeatAPI{}
	a.api = f
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.last.OwnerActive || f.last.AcceptJobsUntil == "" {
		t.Fatal("raw owner state lost")
	}
	if err := a.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := a.hostHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.last.AcceptJobsUntil != "" {
		t.Fatal("paused heartbeat retained permission")
	}
}

func TestManualLocalAPIRequiresAuthAndRejectsBrowser(t *testing.T) {
	for _, tc := range []struct {
		token, origin, body string
		want                int
	}{
		{"", "", "", 401}, {strings.Repeat("x", 64), "https://evil.test", "", 403},
		{strings.Repeat("x", 64), "", `{"seconds":999999}`, 400},
		{strings.Repeat("x", 64), "", "", 200},
	} {
		a, _ := testAgent(t)
		h, err := a.Handler(strings.Repeat("x", 64))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1:8788/v1/accept-jobs", strings.NewReader(tc.body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
		if tc.want != 200 && a.Snapshot().OwnerActivityOverride {
			t.Fatal("unauthorized permission")
		}
	}
}
