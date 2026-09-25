package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

func wakeTestConfig(t *testing.T, coordinator string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "config.json")
	c := config.Config{Version: 1, Coordinator: coordinator, Name: "Mac", HostID: "host1", Listen: "127.0.0.1:8788",
		Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	secrets, err := config.NewSecrets(p, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(context.Background(), "host", "host-token-123456789012345678901234"); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWakeCommandFlags(t *testing.T) {
	p := wakeTestConfig(t, "http://127.0.0.1:1")
	ctx := context.Background()
	for _, args := range [][]string{
		{"--config", p},
		{"--config", p, "--host", "h2", "--mac", "3c:22:fb:01:02:03"},
		{"--config", p, "--mac", "ff:ff:ff:ff:ff:ff"},
		{"--config", p, "--mac", "3c22.fb01.0203"},
		{"--config", p, "--host", "../x"},
		{"--config", p, "--host", "host1"}, // itself
		{"--config", p, "--host", "h2", "extra"},
	} {
		if err := wakeCommand(ctx, args); err == nil {
			t.Errorf("wake %v succeeded; want an error", args)
		}
	}
}

func TestWakeCommandHost(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer host-token-123456789012345678901234" {
			t.Errorf("bad request %s auth=%v", r.Method, r.Header.Get("Authorization") != "")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/hosts/h2/wake":
			_, _ = w.Write([]byte(`{"requested":true,"requestId":"wr_1","relays":1}`))
		case "/api/v2/hosts/h3/wake":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"no_wake_relay","message":"no relay"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	p := wakeTestConfig(t, s.URL)
	ctx := context.Background()
	if err := wakeCommand(ctx, []string{"--config", p, "--host", "h2"}); err != nil {
		t.Fatal(err)
	}
	err := wakeCommand(ctx, []string{"--config", p, "--host", "h3"})
	if err == nil || errorCode(err) != "no_wake_relay" || err.Error() != "No awake neXal Mac on that computer's network can wake it." {
		t.Fatalf("409 no_wake_relay = %v (code %s)", err, errorCode(err))
	}
	err = wakeCommand(ctx, []string{"--config", p, "--host", "h4"})
	if err == nil || errorCode(err) != "wake_unavailable" || strings.Contains(err.Error(), s.URL) {
		t.Fatalf("404 = %v (code %s)", err, errorCode(err))
	}
	if errorCode(os.ErrNotExist) != "connector_error" {
		t.Fatal("uncoded errors must keep the historic connector_error code")
	}
}
