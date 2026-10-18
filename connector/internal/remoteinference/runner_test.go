package remoteinference

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCoord struct {
	mu      sync.Mutex
	tasks   []Task
	reports []Report
}

func (f *fakeCoord) RemoteInferenceTasks(context.Context, string) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Task(nil), f.tasks...), nil
}
func (f *fakeCoord) ReportRemoteInference(_ context.Context, _ string, r Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, r)
	return nil
}
func (f *fakeCoord) last() Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reports) == 0 {
		return Report{}
	}
	return f.reports[len(f.reports)-1]
}

// script writes a fake `nexal` that logs its arguments and behaves per subcommand.
func script(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "nexal")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func wait(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestRunsSetupInstallServeThenReportsEndpoint(t *testing.T) {
	fl, _ := net.Listen("tcp", "127.0.0.1:0")
	p := fl.Addr().(*net.TCPAddr).Port
	fl.Close()
	port := strconv.Itoa(p)
	go func() {
		time.Sleep(500 * time.Millisecond)
		if l, err := net.Listen("tcp", "127.0.0.1:"+port); err == nil {
			go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
		}
	}()
	exe := script(t, `[ "$2" = serve ] && exec sleep 30; exit 0`)
	c := &fakeCoord{tasks: []Task{{ID: "rinf_1", ModelID: "qwen3-4b", State: "pending"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1/32" }, Port: p, MaxPort: p, ReadyAfter: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Once(ctx)
	wait(t, func() bool { return c.last().State == "ready" })
	if got := c.last(); got.Endpoint != "http://127.0.0.1:"+port+"/v1" || got.Progress == nil || *got.Progress != 1 {
		t.Fatalf("%+v", got)
	}
	steps := map[string]bool{}
	c.mu.Lock()
	for _, rp := range c.reports {
		if rp.Progress != nil {
			steps[rp.Step] = true
		}
	}
	c.mu.Unlock()
	for _, want := range []string{"setup", "pin", "install", "serve"} {
		if !steps[want] {
			t.Fatalf("no progress reported for step %q: %v", want, steps)
		}
	}
	// Cancelled request: the server is stopped.
	c.mu.Lock()
	c.tasks = nil
	c.mu.Unlock()
	r.Once(ctx)
	r.mu.Lock()
	n := len(r.serving)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("server still tracked: %d", n)
	}
}

func TestInstallFailureIsReported(t *testing.T) {
	exe := script(t, `[ "$2" = install ] && { echo "model not pinned"; exit 1; }; exit 0`)
	c := &fakeCoord{tasks: []Task{{ID: "rinf_2", ModelID: "x", State: "pending"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1" }}
	r.Once(context.Background())
	wait(t, func() bool { return c.last().State == "failed" })
	if g := c.last(); g.Step != "install" || g.Detail != "model not pinned" {
		t.Fatalf("%+v", g)
	}
}

func TestNoAddressFails(t *testing.T) {
	c := &fakeCoord{tasks: []Task{{ID: "rinf_3", ModelID: "x"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: "/nonexistent", Address: func() string { return "" }}
	r.Once(context.Background())
	wait(t, func() bool { return c.last().State == "failed" })
}

func TestInstallReportsProgress(t *testing.T) {
	exe := script(t, `if [ "$2" = install ]; then
echo '{"event":"start","modelId":"m","totalBytes":1000}'
echo '{"event":"progress","file":"a","bytes":500,"total":1000,"overallBytes":500,"overallTotal":1000}'
echo '{"event":"progress","file":"a","bytes":1000,"total":1000,"overallBytes":1000,"overallTotal":1000}'
echo '{"event":"done"}'
fi; exit 0`)
	c := &fakeCoord{tasks: []Task{{ID: "rinf_4", ModelID: "m"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "" }}
	r.init()
	if msg := r.install(context.Background(), c.tasks[0], []string{"install", "m"}); msg != "" {
		t.Fatal(msg)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reports) != 2 {
		t.Fatalf("%+v", c.reports)
	}
	if p := c.reports[0].Progress; p == nil || *p < 0.46 || *p > 0.48 || c.reports[0].Step != "install" {
		t.Fatalf("download report %+v", c.reports[0])
	}
	if p := c.reports[1].Progress; p == nil || *p != 0.85 || c.reports[1].Step != "verify" {
		t.Fatalf("verify report %+v", c.reports[1])
	}
}

func TestInstallFailureUsesTheErrorEvent(t *testing.T) {
	exe := script(t, `echo '{"event":"error","code":"x","message":"not enough disk space"}'; exit 1`)
	r := &Runner{Coord: &fakeCoord{}, HostID: "h", Exe: exe}
	r.init()
	if msg := r.install(context.Background(), Task{ID: "x", ModelID: "m"}, []string{"install", "m"}); msg != "not enough disk space" {
		t.Fatal(msg)
	}
}

func TestAlreadyInstalledModelSkipsPinAndDownload(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	exe := script(t, `echo "$2" >> `+log+`
if [ "$2" = status ]; then echo '{"models":[{"modelId":"qwen3-4b-q4"}]}'; fi
[ "$2" = serve ] && exec sleep 30
exit 0`)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	go func() {
		time.Sleep(500 * time.Millisecond)
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p)); err == nil {
			go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
		}
	}()
	c := &fakeCoord{tasks: []Task{{ID: "rinf_5", ModelID: "qwen3-4b-q4"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1" }, Port: p, MaxPort: p, ReadyAfter: 10 * time.Second}
	r.Once(context.Background())
	wait(t, func() bool { return c.last().State == "ready" })
	b, _ := os.ReadFile(log)
	calls := string(b)
	if strings.Contains(calls, "pin") || strings.Contains(calls, "install") || !strings.Contains(calls, "serve") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestPoolMemberWaitsForRingThenServes(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	exe := script(t, `echo "$@" >> `+log+`
if [ "$2" = status ]; then echo '{"models":[{"modelId":"big"}]}'; fi
[ "$2" = pool-serve ] && exec sleep 30
exit 0`)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	ap := l.Addr().(*net.TCPAddr).Port
	l.Close()
	// Answer on whichever API port the runner hands to pool-serve.
	go func() {
		for i := 0; i < 200; i++ {
			time.Sleep(100 * time.Millisecond)
			b, _ := os.ReadFile(log)
			_, rest, ok := strings.Cut(string(b), "--api-port ")
			if !ok {
				continue
			}
			if l, err := net.Listen("tcp", "127.0.0.1:"+strings.Fields(rest)[0]); err == nil {
				go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
			}
			return
		}
	}()
	task := Task{ID: "rinf_p", ModelID: "big", PoolID: "pool_1", PoolRank: 0, PoolSize: 2}
	c := &fakeCoord{tasks: []Task{task}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1" }, Port: ap, MaxPort: ap + 1, ReadyAfter: 20 * time.Second, PoolWait: 20 * time.Second}
	r.Once(context.Background())
	wait(t, func() bool { return c.last().RingAddress != "" })
	ringAddr := c.last().RingAddress
	if !strings.HasPrefix(ringAddr, "127.0.0.1:") {
		t.Fatal(ringAddr)
	}
	// The coordinator now knows everyone: the next poll carries the ring.
	c.mu.Lock()
	c.tasks[0].PoolRing = []string{ringAddr, "127.0.0.1:9"}
	c.mu.Unlock()
	r.Once(context.Background())
	wait(t, func() bool { return c.last().State == "ready" })
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "pool-serve big --rank 0 --hosts "+ringAddr+",127.0.0.1:9") {
		t.Fatalf("calls:\n%s", b)
	}
	if !strings.Contains(c.last().Endpoint, "http://127.0.0.1:") {
		t.Fatal(c.last())
	}
}

func TestInstallTreatsAlreadyInstalledAsSuccess(t *testing.T) {
	exe := script(t, `echo '{"event":"error","code":"already_installed","message":"qwen3-4b-q4 is already installed"}'; exit 1`)
	r := &Runner{Coord: &fakeCoord{}, HostID: "h", Exe: exe}
	r.init()
	if msg := r.install(context.Background(), Task{ID: "x", ModelID: "m"}, []string{"install", "m"}); msg != "" {
		t.Fatal(msg)
	}
}

func TestUnreviewedModelGetsAPlainMessage(t *testing.T) {
	exe := script(t, `[ "$2" = install ] && { echo '{"event":"error","code":"not_installable","message":"x cannot be installed: not pinned: missing revision"}'; exit 1; }; exit 0`)
	c := &fakeCoord{tasks: []Task{{ID: "rinf_u", ModelID: "x"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1" }}
	r.Once(context.Background())
	wait(t, func() bool { return c.last().State == "failed" })
	if d := c.last().Detail; !strings.Contains(d, "not been reviewed") || strings.Contains(d, "pinned") {
		t.Fatal(d)
	}
}

func TestPinStepNeverRunsAPinCommand(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	exe := script(t, `echo "$2" >> `+log+`; [ "$2" = install ] && exit 1; exit 0`)
	c := &fakeCoord{tasks: []Task{{ID: "rinf_n", ModelID: "x"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1" }}
	r.Once(context.Background())
	wait(t, func() bool { return c.last().State == "failed" })
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "pin") {
		t.Fatalf("calls:\n%s", b)
	}
}
