package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExitRouteRejectsUnsafeFlagsBeforeCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"--tunnel", "8.8.8.8"},
		{"--target-device", "22222222-2222-4222-8222-222222222222"},
		{"--disable", "--target-device", "bad-id"},
		{"--disable"},
	} {
		args = append(args, "--config", "/private/tmp/nonexistent-nexal-test-config.json")
		if err := exitRouteCommand(context.Background(), args); err == nil || strings.Contains(err.Error(), "configuration") {
			t.Fatalf("invalid flags not rejected before reading config: %v: %v", args, err)
		}
	}
}

func TestExitRoutePendingIsNotReportedAsConfigured(t *testing.T) {
	err := exitRouteError(&client.StatusError{Status: 409, Code: "exit_route_pending"})
	if errorCode(err) != "exit_route_pending" || !strings.Contains(err.Error(), "still being applied") {
		t.Fatal("pending state not explained", err)
	}
}

func TestExitRouteErrorMapsKnownCodesAndPassesOthersThrough(t *testing.T) {
	for _, code := range []string{
		"exit_source_unavailable",
		"exit_target_unavailable",
		"exit_peer_mapping_pending",
		"exit_route_unavailable",
		"exit_route_pending",
		"exit_route_superseded",
	} {
		raw := &client.StatusError{Status: 409, Code: code}
		err := exitRouteError(raw)
		if errorCode(err) != code || err.Error() == raw.Error() || err.Error() == "" {
			t.Fatalf("%s not mapped to a friendly coded error: %v", code, err)
		}
	}
	unknown := &client.StatusError{Status: 500, Code: "something_else"}
	if got := exitRouteError(unknown); got != error(unknown) {
		t.Fatalf("unknown status code was rewritten: %v", got)
	}
	plain := errors.New("dial failed")
	if got := exitRouteError(plain); got != plain {
		t.Fatalf("non-status error was rewritten: %v", got)
	}
}

func TestExitRouteRequiresEnrolledHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	err := exitRouteCommand(context.Background(), []string{"--tunnel", "100.64.0.2", "--config", path})
	if err == nil || !strings.Contains(err.Error(), "enroll this host") {
		t.Fatalf("unenrolled host not refused before credential access: %v", err)
	}
}

func TestExitRouteAcceptsValidDisableFormsThenRequiresEnrollment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--disable", "--tunnel", "100.64.0.2"},
		{"--disable", "--target-device", "22222222-2222-4222-8222-222222222222"},
		{"--disable", "--target-device", "AAAAAAAA-2222-4222-8222-222222222222"},
	} {
		err := exitRouteCommand(context.Background(), append(args, "--config", path))
		if err == nil || !strings.Contains(err.Error(), "enroll this host") {
			t.Fatalf("valid flags %v did not reach the enrollment check: %v", args, err)
		}
	}
}

func TestExitRouteReportsFlagParseAndMissingConfigErrors(t *testing.T) {
	if err := exitRouteCommand(context.Background(), []string{"--no-such-flag"}); err == nil {
		t.Fatal("unknown flag was accepted")
	}
	missing := filepath.Join(t.TempDir(), "private", "config.json")
	err := exitRouteCommand(context.Background(), []string{"--tunnel", "100.64.0.2", "--config", missing})
	if err == nil || strings.Contains(err.Error(), "enroll this host") {
		t.Fatalf("missing config was not reported as a load error: %v", err)
	}
}

// End to end against a fake coordinator: the enable and disable-by-device
// requests must carry the stored host bearer and the right body, the
// acknowledgement must reach stdout, and a coordinator refusal must surface as
// the friendly coded error rather than a raw status.
func TestExitRouteEndToEndAgainstFakeCoordinator(t *testing.T) {
	const source = "11111111-1111-4111-8111-111111111111"
	const target = "22222222-2222-4222-8222-222222222222"
	var mode string // "ok" or "refuse"
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		if mode == "refuse" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"exit_target_unavailable","message":"raw"}}`))
			return
		}
		enabled, _ := gotBody["enabled"].(bool)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schemaVersion": 1, "networkId": "n1", "sourceDeviceId": source, "targetDeviceId": target,
			"routeId": "nx-exit-" + strings.Repeat("a", 32), "enabled": enabled, "configured": enabled,
			"selectionRequired": enabled, "autoApply": false, "readiness": map[bool]string{true: "awaiting_client_verification", false: "not_configured"}[enabled],
			"ipv6": "provider_managed",
		})
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	seed := config.Config{Version: 1, Coordinator: srv.URL, Name: "Mac", Listen: "127.0.0.1:8788", HostID: "host-1",
		Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	if err := config.Save(cfg, seed); err != nil {
		t.Fatal(err)
	}
	if err := (config.FileSecrets{Dir: filepath.Join(dir, "dev-secrets")}).Put(context.Background(), "host", "tok1234567890abcdef"); err != nil {
		t.Fatal(err)
	}

	mode = "ok"
	var err error
	out := captureStdout(t, func() {
		err = exitRouteCommand(context.Background(), []string{"--config", cfg, "--tunnel", "100.64.0.2"})
	})
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if gotAuth != "Bearer tok1234567890abcdef" || gotPath != "/api/hosts/host-1/exit-route" ||
		gotBody["enabled"] != true || gotBody["targetTunnelAddress"] != "100.64.0.2" {
		t.Fatalf("enable request wrong: auth=%q path=%q body=%v", gotAuth, gotPath, gotBody)
	}
	if !strings.Contains(out, `"readiness":"awaiting_client_verification"`) {
		t.Fatalf("ack not emitted: %q", out)
	}

	out = captureStdout(t, func() {
		err = exitRouteCommand(context.Background(), []string{"--config", cfg, "--disable", "--target-device", strings.ToUpper(target)})
	})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if gotBody["enabled"] != false || gotBody["targetDeviceId"] != target || gotBody["targetTunnelAddress"] != nil {
		t.Fatalf("disable body wrong (device id must be lowercased and replace the address): %v", gotBody)
	}
	if !strings.Contains(out, `"readiness":"not_configured"`) {
		t.Fatalf("disable ack not emitted: %q", out)
	}

	mode = "refuse"
	err = exitRouteCommand(context.Background(), []string{"--config", cfg, "--tunnel", "100.64.0.2"})
	if errorCode(err) != "exit_target_unavailable" || strings.Contains(err.Error(), "raw") {
		t.Fatalf("refusal not mapped to friendly coded error: %v", err)
	}
}
