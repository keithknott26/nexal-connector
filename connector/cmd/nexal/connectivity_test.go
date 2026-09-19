package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nexal/connector/internal/config"
)

func TestCoordinatorCheckWithoutCredentialStore(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/health" || r.Method != "GET" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected or authenticated request")
		}
		_, _ = io.WriteString(w, `{"status":"ok","mode":"development","version":"0.1"}`)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: srv.URL, Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := run(context.Background(), []string{"coordinator-check", "--config", path}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || hits != 1 {
		t.Fatal("preflight mutated config or performed unexpected requests")
	}
	for _, args := range [][]string{
		{"coordinator-check", "--config", path, "--token", "secret"},
		{"coordinator-check", "--config", "relative"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Fatal("unsafe flags accepted")
		}
	}
}
