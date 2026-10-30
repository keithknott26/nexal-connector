package remoteinference

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sse(n int) string {
	var b strings.Builder
	b.WriteString("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	for i := 0; i < n-1; i++ {
		b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestTallyCountsStreamedTokensAcrossAnyChunking(t *testing.T) {
	h := http.Header{"Content-Type": {"text/event-stream"}}
	body := []byte(sse(7))
	for _, size := range []int{1, 3, 7, 11, len(body)} {
		var tl tally
		got := 0
		for i := 0; i < len(body); i += size {
			end := i + size
			if end > len(body) {
				end = len(body)
			}
			got += tl.feed(body[i:end], h)
		}
		if got != 7 {
			t.Fatalf("chunks of %d: counted %d tokens, want 7 ([DONE] is not one)", size, got)
		}
	}
}

func TestTallyReadsUsageFromAnAnswerThatIsNotStreamed(t *testing.T) {
	h := http.Header{"Content-Type": {"application/json"}}
	var tl tally
	if n := tl.feed([]byte(`{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":42}}`), h); n != 0 {
		t.Fatal("a plain answer is counted at its end")
	}
	if n := tl.finish(h); n != 42 {
		t.Fatalf("want the usage count 42, got %d", n)
	}
	var est tally
	est.feed([]byte(strings.Repeat("a", 400)), h)
	if n := est.finish(h); n != 100 {
		t.Fatalf("without usage, about four bytes a token: got %d", n)
	}
}

func TestMeterKeepsLocalAndOffloadApartInSteps(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	m := NewMeter()
	m.now = func() time.Time { return clock }
	m.Add(Local, 50)
	m.Done(Local, 0, "")
	clock = clock.Add(5 * time.Second)
	m.Add(Offload, 100)
	m.Done(Offload, 0, "RunPod GPU")
	s := m.Snapshot()
	if len(s.Points) != 120 || s.BucketSeconds != 5 || s.OffloadName != "RunPod GPU" {
		t.Fatalf("window: %d points, %d s steps, offload %q", len(s.Points), s.BucketSeconds, s.OffloadName)
	}
	prev, last := s.Points[len(s.Points)-2], s.Points[len(s.Points)-1]
	if prev.LocalTPS != 10 || prev.OffloadTPS != 0 || prev.LocalReqs != 1 {
		t.Fatalf("local step wrong: %+v", prev)
	}
	if last.OffloadTPS != 20 || last.LocalTPS != 0 || last.OffReqs != 1 {
		t.Fatalf("offload step wrong: %+v", last)
	}
	if s.Totals.LocalTokens != 50 || s.Totals.OffloadTokens != 100 || s.Totals.OffloadRequests != 1 {
		t.Fatalf("totals: %+v", s.Totals)
	}
	clock = clock.Add(2 * time.Hour) // old steps fall out
	m.Add(Local, 1)
	if len(m.buckets) > int(meterKeep/meterBucket)+1 {
		t.Fatalf("history is not bounded: %d", len(m.buckets))
	}
	var nilMeter *Meter
	nilMeter.Add(Local, 1) // a forwarder with no meter must not crash
	nilMeter.Done(Offload, 1, "x")
}

func TestForwarderMeasuresLocalAndOffloadedAnswersSeparately(t *testing.T) {
	stream := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(n))
		}
	}
	local := httptest.NewServer(stream(5))
	t.Cleanup(local.Close)
	gpu := httptest.NewServer(stream(9))
	t.Cleanup(gpu.Close)
	f, err := NewForwarder("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	f.SetUpstream(strings.TrimPrefix(local.URL, "http://"), "")
	o := &Overflow{Enabled: true, GPUPeers: map[string]bool{"*": true}, HardAlias: "qwen-large",
		GPU: &Tier{Name: "RunPod GPU", BaseURL: gpu.URL + "/v1", Model: "big", FirstByte: time.Second}}
	m := NewMeter()
	f.SetOverflow(o)
	f.SetMeter(m)
	o.SetMeter(m)
	chat(t, f, "qwen3-4b")   // this Mac
	chat(t, f, "qwen-large") // asked for the GPU
	// chat reads only the first chunk, so the handler may still be finishing its count; wait for both requests.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if tot := m.Snapshot().Totals; tot.LocalRequests == 1 && tot.OffloadRequests == 1 {
			break
		}
	}
	tot := m.Snapshot().Totals
	if tot.LocalTokens != 5 || tot.LocalRequests != 1 || tot.OffloadTokens != 9 || tot.OffloadRequests != 1 {
		t.Fatalf("totals: %+v", tot)
	}
	if m.Snapshot().OffloadName != "RunPod GPU" {
		t.Fatal("the offload tier's name is shown")
	}
}

func TestAnErrorIsNotCountedAsAnAnswer(t *testing.T) {
	f, err := NewForwarder("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	m := NewMeter()
	f.SetMeter(m)
	f.SetUpstream("", "The model is starting.") // every request gets a 503 with a message
	chat(t, f, "qwen3-4b")
	if tot := m.Snapshot().Totals; tot.LocalTokens != 0 || tot.LocalRequests != 0 {
		t.Fatalf("a 503 must not count: %+v", tot)
	}
}

func TestPublishWritesAPrivateSnapshotAndStops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inference-throughput.json")
	m := NewMeter()
	m.Add(Local, 10)
	m.Done(Local, 0, "")
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { m.Publish(done, path, 20*time.Millisecond); close(stopped) }()
	var s Snapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &s) == nil && s.Totals.LocalTokens == 10 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.Totals.LocalTokens != 10 || len(s.Points) != 120 {
		t.Fatalf("snapshot not published: %+v", s.Totals)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot must be private, mode %v", st.Mode().Perm())
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish did not stop")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".inference-throughput-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
