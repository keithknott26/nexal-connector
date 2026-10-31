package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"nexal/connector/internal/config"
	"nexal/connector/internal/runpod"
)

const rpKey = "rpa_0123456789abcdefghij"

type rpFake struct {
	mu    sync.Mutex
	paths []string
	gone  bool // GET/DELETE answer 404
}

func (f *rpFake) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// runPodEnv points the command at a temporary configuration and a fake RunPod.
func runPodEnv(t *testing.T) (path string, f *rpFake) {
	f = &rpFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		gone := f.gone
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/templates":
			w.Write([]byte(`{"id":"tpl1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/endpoints":
			w.Write([]byte(`{"id":"ep1"}`))
		case gone:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Write([]byte(`{"id":"ep1"}`))
		}
	}))
	t.Cleanup(srv.Close)
	old := newRunPod
	newRunPod = func(key string) *runpod.Client { return &runpod.Client{Base: srv.URL, Key: key} }
	t.Cleanup(func() { newRunPod = old })
	dir := t.TempDir()
	path = filepath.Join(dir, "private", "config.json")
	c := config.Config{Version: 1, Coordinator: srv.URL, Name: "Mac", HostID: "host1", Listen: "127.0.0.1:8788", Development: true, DevSecrets: true,
		Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	return path, f
}

func putKey(t *testing.T, path string) {
	t.Helper()
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.NewSecrets(path, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), "overflow-gpu", rpKey); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionFillsTheGPUTierAndTurnsLongPromptOffloadOn(t *testing.T) {
	path, f := runPodEnv(t)
	ctx := context.Background()
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "standard", maxWorkers: 1}); err == nil ||
		!strings.Contains(err.Error(), "API key") {
		t.Fatalf("without a key it must say so: %v", err)
	}
	if len(f.log()) != 0 {
		t.Fatal("no request without a key")
	}
	putKey(t, path)
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "standard", maxWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	c, _ := config.Load(path)
	o := c.Overflow
	if o == nil || o.RunPod == nil || o.RunPod.EndpointID != "ep1" || o.RunPod.TemplateID != "tpl1" {
		t.Fatalf("record not saved: %+v", o)
	}
	if o.GPU == nil || o.GPU.BaseURL != "https://api.runpod.ai/v2/ep1/openai/v1" || o.GPU.Model != "Qwen/Qwen3-32B-AWQ" || o.GPU.FirstByteSeconds != 180 {
		t.Fatalf("tier not configured: %+v", o.GPU)
	}
	// One-click setup (the owner's two keys and nothing else) switches long-prompt offload on for every peer, with the
	// data-exposure notice shown in the app; it never chooses "always" for the owner.
	if !o.Enabled || o.Always || len(o.GPUPeers) != 1 || o.GPUPeers[0] != "*" || len(o.CloudPeers) != 0 || o.MaxPromptTokens != 6000 {
		t.Fatalf("setting up must turn on long-prompt offload only: %+v", o)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), rpKey) {
		t.Fatal("the API key must never be written to the configuration")
	}
	// A second click changes nothing and creates nothing.
	before := len(f.log())
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "standard", maxWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	if got := f.log()[before:]; len(got) != 1 || got[0] != "GET /endpoints/ep1" {
		t.Fatalf("an existing endpoint must only be checked: %v", got)
	}
}

func TestProvisionReplacesAnEndpointThatWasDeletedInRunPod(t *testing.T) {
	path, f := runPodEnv(t)
	putKey(t, path)
	ctx := context.Background()
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "light", maxWorkers: 2}); err != nil {
		t.Fatal(err)
	}
	// The owner deletes the endpoint in RunPod's console: it answers 404 from now on.
	f.mu.Lock()
	f.gone = true
	f.paths = nil
	f.mu.Unlock()
	if err := overflowRunPod(ctx, "check", path, runPodOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "light", maxWorkers: 2}); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(f.log(), ",")
	if !strings.Contains(log, "GET /endpoints/ep1") || !strings.Contains(log, "POST /templates") || !strings.Contains(log, "POST /endpoints") {
		t.Fatalf("want a check, then a new endpoint: %s", log)
	}
}

func TestDeprovisionRemovesEverythingAndTurnsOffloadingOffWhenNothingIsLeft(t *testing.T) {
	path, f := runPodEnv(t)
	putKey(t, path)
	ctx := context.Background()
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "standard", maxWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	c, _ := config.Load(path)
	c.Overflow.Enabled = true
	c.Overflow.GPUPeers = []string{"*"}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if err := overflowRunPod(ctx, "deprovision", path, runPodOptions{}); err != nil {
		t.Fatal(err)
	}
	log := f.log()
	if n := len(log); n < 2 || log[n-2] != "DELETE /endpoints/ep1" || log[n-1] != "DELETE /templates/tpl1" {
		t.Fatalf("endpoint must be deleted before its template: %v", log)
	}
	c, _ = config.Load(path)
	if c.Overflow.RunPod != nil || c.Overflow.GPU != nil || c.Overflow.Enabled {
		t.Fatalf("nothing should be left configured: %+v", c.Overflow)
	}
}

func TestForgetNeedsNoKeyAndNoNetwork(t *testing.T) {
	path, f := runPodEnv(t)
	putKey(t, path)
	ctx := context.Background()
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "standard", maxWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	n := len(f.log())
	if err := overflowRunPod(ctx, "deprovision", path, runPodOptions{forget: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.log()) != n {
		t.Fatal("--forget must not call RunPod")
	}
	if c, _ := config.Load(path); c.Overflow.RunPod != nil {
		t.Fatal("record should be dropped")
	}
}

func TestCustomPresetNeedsAModelAndGPUs(t *testing.T) {
	path, f := runPodEnv(t)
	putKey(t, path)
	ctx := context.Background()
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "custom", maxWorkers: 1}); err == nil {
		t.Fatal("custom without a model must fail")
	}
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "bogus", maxWorkers: 1}); err == nil {
		t.Fatal("unknown preset must fail")
	}
	if len(f.log()) != 0 {
		t.Fatal("invalid options must not reach RunPod")
	}
	if err := overflowRunPod(ctx, "provision", path, runPodOptions{preset: "custom", model: "org/model", gpuTypes: []string{"NVIDIA A40"}, maxWorkers: 1}); err != nil {
		t.Fatal(err)
	}
}
