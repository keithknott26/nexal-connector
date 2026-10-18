package remoteinference

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	exe := script(t, `[ "$2" = serve ] && exec sleep 30; exit 0`)
	c := &fakeCoord{tasks: []Task{{ID: "rinf_1", ModelID: "qwen3-4b", State: "pending"}}}
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1/32" }, Port: p, ReadyAfter: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Once(ctx)
	wait(t, func() bool { return c.last().State == "ready" })
	if got := c.last(); got.Endpoint != "http://127.0.0.1:"+port+"/v1" {
		t.Fatalf("%+v", got)
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
