package remoteinference

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func chat(t *testing.T, f *Forwarder, model string) (*http.Response, string) {
	t.Helper()
	r, err := http.Post("http://"+f.addrs[0]+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b := make([]byte, 512)
	n, _ := r.Body.Read(b)
	return r, string(b[:n])
}

func setup(t *testing.T, gpu, cloud http.HandlerFunc) (*Forwarder, *Overflow) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("local")) }))
	t.Cleanup(local.Close)
	g := httptest.NewServer(gpu)
	t.Cleanup(g.Close)
	c := httptest.NewServer(cloud)
	t.Cleanup(c.Close)
	f, err := NewForwarder("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	f.SetUpstream(strings.TrimPrefix(local.URL, "http://"), "")
	o := &Overflow{Enabled: true, GPUPeers: map[string]bool{"*": true}, CloudPeers: map[string]bool{"*": true}, HardAlias: "qwen-large",
		GPU:   &Tier{Name: "gpu", BaseURL: g.URL + "/v1", Model: "qwen3-32b", FirstByte: 200 * time.Millisecond},
		Cloud: &Tier{Name: "cloud", BaseURL: c.URL + "/v1", Model: "gpt", APIKey: "k"}}
	f.SetOverflow(o)
	return f, o
}

func ok(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(s)) }
}

func TestOverflowEasyStaysLocal(t *testing.T) {
	f, _ := setup(t, ok("gpu"), ok("cloud"))
	if _, b := chat(t, f, "qwen3-4b"); b != "local" {
		t.Fatal(b)
	}
}

func TestOverflowHardAliasGoesToGPUAndShowsIt(t *testing.T) {
	f, _ := setup(t, ok("gpu"), ok("cloud"))
	r, b := chat(t, f, "qwen-large")
	if b != "gpu" || r.Header.Get("x-nexal-backend") != "gpu" {
		t.Fatal(b, r.Header)
	}
}

func TestOverflowSlowGPUFallsToCloud(t *testing.T) {
	f, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) }, ok("cloud"))
	if r, b := chat(t, f, "qwen-large"); b != "cloud" || r.Header.Get("x-nexal-backend") != "cloud" {
		t.Fatal(b)
	}
}

func TestOverflowNoCloudConsentNeverReachesCloud(t *testing.T) {
	var hit int32
	f, o := setup(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) },
		func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hit, 1) })
	o.CloudPeers = map[string]bool{}
	if r, _ := chat(t, f, "qwen-large"); r.StatusCode != 502 || hit != 0 {
		t.Fatal(r.StatusCode, hit)
	}
}

func TestOverflowQueueWaitGoesToGPUAndFailureFallsBackLocal(t *testing.T) {
	f, o := setup(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, ok("cloud"))
	o.Cloud, o.MaxWait, o.avg = nil, time.Second, 5*time.Second
	f.inflight = 2
	if _, b := chat(t, f, "qwen3-4b"); b != "local" {
		t.Fatal(b)
	}
}

func TestOverflowDisabledAndPeerNotAllowed(t *testing.T) {
	f, o := setup(t, ok("gpu"), ok("cloud"))
	o.GPUPeers, o.CloudPeers = map[string]bool{"10.9.9.9": true}, nil
	if r, _ := chat(t, f, "qwen-large"); r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
	o.Enabled = false
	if _, b := chat(t, f, "qwen-large"); b != "local" {
		t.Fatal(b)
	}
}

func TestOverflowDailyLimit(t *testing.T) {
	f, o := setup(t, ok("gpu"), ok("cloud"))
	o.DailyLimit = 1
	chat(t, f, "qwen-large")
	if r, _ := chat(t, f, "qwen-large"); r.StatusCode != 429 {
		t.Fatal(r.StatusCode)
	}
}
