package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/remoteinference"
)

func shareableGPUClient(t *testing.T, tasks []remoteinference.Task, status int) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 && status != http.StatusOK {
			http.Error(w, "boom", status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": tasks})
	}))
	t.Cleanup(srv.Close)
	api, err := client.New(srv.URL, "tok-0123456789abcdef", true)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestShareableGPU(t *testing.T) {
	ctx := context.Background()
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	cfg := config.Config{HostID: "host-1", Overflow: &config.OverflowConfig{GPU: &config.OverflowTier{
		Name: "", BaseURL: "https://gpu.example/v1", Model: "m", FirstByteSeconds: 7}}}
	secrets := memSecrets{"overflow-gpu": "gpu-api-key"}
	tasks := []remoteinference.Task{
		{ID: "waiting"},
		{ID: "ready", OffloadKey: remoteinference.PublicKey(priv)},
		{ID: "bad", OffloadKey: "not-a-key"},
	}
	api := shareableGPUClient(t, tasks, 0)

	cases := []struct {
		name    string
		api     *client.Client
		cfg     config.Config
		secrets memSecrets
		task    string
		reason  string
	}{
		{"no tier", api, config.Config{HostID: "host-1"}, secrets, "ready", "this Mac has no GPU tier to lend"},
		{"no key", api, cfg, memSecrets{}, "ready", "this Mac has no GPU API key to lend"},
		{"unreadable", shareableGPUClient(t, nil, http.StatusInternalServerError), cfg, secrets, "ready", "could not read the request"},
		{"unknown task", api, cfg, secrets, "missing", "no such request"},
		{"no peer key yet", api, cfg, secrets, "waiting", "waiting for that Mac's key"},
		{"unusable peer key", api, cfg, secrets, "bad", "that Mac's key is not usable"},
	}
	for _, c := range cases {
		got, reason := shareableGPU(ctx, c.api, c.cfg, c.secrets, c.task)
		if got != nil || reason != c.reason {
			t.Errorf("%s: got (%v, %q), want (nil, %q)", c.name, got, reason, c.reason)
		}
	}

	got, reason := shareableGPU(ctx, api, cfg, secrets, "ready")
	if got == nil || reason != "" {
		t.Fatalf("ready task must be shareable, got (%v, %q)", got, reason)
	}
	if got.Name != "the GPU" || got.BaseURL != "https://gpu.example/v1" || got.Model != "m" || got.FirstByteSeconds != 7 {
		t.Fatalf("tier fields not carried over: %+v", got)
	}
	opened, err := remoteinference.Open(priv, got.SenderKey, got.SealedKey)
	if err != nil || opened != "gpu-api-key" {
		t.Fatalf("sealed key must open to the API key with the peer's private key: %q, %v", opened, err)
	}
}
