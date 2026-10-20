package remoteinference

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// poolRunner is a runner whose `nexal` is a script that logs its calls and keeps pool servers "running".
func poolRunner(t *testing.T, c *fakeCoord) (*Runner, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls")
	exe := script(t, `echo "$@" >> `+log+`
if [ "$2" = status ]; then echo '{"models":[{"modelId":"big"}]}'; fi
[ "$2" = pool-serve ] && exec sleep 60
exit 0`)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	ap := l.Addr().(*net.TCPAddr).Port
	l.Close()
	r := &Runner{Coord: c, HostID: "h", Exe: exe, Address: func() string { return "127.0.0.1" }, Port: ap, MaxPort: ap + 20,
		ReadyAfter: 20 * time.Second, PoolPoll: 100 * time.Millisecond, Heartbeat: 200 * time.Millisecond, FormWait: 10 * time.Second,
		StallAfter: 5 * time.Second, ProbeEvery: 100 * time.Millisecond, Rejoin: 100 * time.Millisecond, RingGrace: 300 * time.Millisecond, WakeGrace: 500 * time.Millisecond}
	return r, log
}

// answerFor makes every `--api-port N` in the call log whose line passes ok answer HTTP, saying which port it is.
func answerFor(t *testing.T, log string, ok func(line string) bool) {
	t.Helper()
	done := map[string]bool{}
	var ls []net.Listener
	stop := make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		for _, l := range ls {
			l.Close()
		}
	})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(30 * time.Millisecond):
			}
			b, _ := os.ReadFile(log)
			for _, line := range strings.Split(string(b), "\n") {
				_, rest, found := strings.Cut(line, "--api-port ")
				if !found || !ok(line) {
					continue
				}
				port := strings.Fields(rest)[0]
				if done[port] {
					continue
				}
				done[port] = true
				l, err := net.Listen("tcp", "127.0.0.1:"+port)
				if err != nil {
					continue
				}
				ls = append(ls, l)
				go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "port "+port) }))
			}
		}
	}()
}

func (f *fakeCoord) find(match func(Report) bool) (Report, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reports) - 1; i >= 0; i-- {
		if match(f.reports[i]) {
			return f.reports[i], true
		}
	}
	return Report{}, false
}

func (f *fakeCoord) setRing(ring []string) {
	f.mu.Lock()
	f.tasks[0].PoolRing = ring
	f.mu.Unlock()
}

func body(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func logHas(log, s string) bool { b, _ := os.ReadFile(log); return strings.Contains(string(b), s) }

func TestPoolLeaderServesAloneThenSharesThenFallsBack(t *testing.T) {
	c := &fakeCoord{tasks: []Task{{ID: "rinf_p", ModelID: "big", PoolID: "pool_1", PoolRank: 0, PoolSize: 2}}}
	r, log := poolRunner(t, c)
	answerFor(t, log, func(string) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Once(ctx)

	// 1. It answers on its own first, and offers to form the pool.
	wait(t, func() bool { return c.last().State == "ready" && c.last().Endpoint != "" })
	if !strings.Contains(c.last().Detail, "alone") || !logHas(log, "pool-serve big --solo --api-host 127.0.0.1") {
		t.Fatal(c.last(), "\n", logHas(log, "x"))
	}
	endpoint := c.last().Endpoint
	var ring string
	wait(t, func() bool {
		rep, ok := c.find(func(r Report) bool { return r.RingAddress != nil && *r.RingAddress != "" })
		if ok {
			ring = *rep.RingAddress
		}
		return ok
	})
	soloBody := body(t, endpoint+"/models")
	if !strings.HasPrefix(soloBody, "port ") {
		t.Fatal(soloBody)
	}

	// 2. Every Mac is ready: the model moves onto the ring and the same address now reaches that server.
	c.setRing([]string{ring, "127.0.0.1:9"})
	wait(t, func() bool { return logHas(log, "--rank 0 --hosts "+ring+",127.0.0.1:9") })
	wait(t, func() bool { return strings.Contains(c.last().Detail, "2 Macs sharing") })
	if now := body(t, endpoint+"/models"); now == soloBody || !strings.HasPrefix(now, "port ") {
		t.Fatalf("still on the solo server: %q", now)
	}

	// 3. A Mac is lost: say so, leave the ring, and serve alone again.
	c.setRing(nil)
	wait(t, func() bool {
		_, ok := c.find(func(r Report) bool { return strings.Contains(r.Detail, "lost a member") })
		return ok
	})
	if _, ok := c.find(func(r Report) bool { return r.RingAddress != nil && *r.RingAddress == "" }); !ok {
		t.Fatal("the leader should take itself out of the ring")
	}
	wait(t, func() bool {
		return c.last().State == "ready" && strings.Contains(c.last().Detail, "serving from this Mac alone")
	})
	b, _ := os.ReadFile(log)
	if strings.Count(string(b), "--solo") != 2 {
		t.Fatalf("expected the model to be started on this Mac twice:\n%s", b)
	}
	if got := body(t, endpoint+"/models"); !strings.HasPrefix(got, "port ") {
		t.Fatal(got)
	}
}

func TestPoolLeaderKeepsServingWhenThePoolDoesNotForm(t *testing.T) {
	c := &fakeCoord{tasks: []Task{{ID: "rinf_p", ModelID: "big", PoolID: "pool_1", PoolRank: 0, PoolSize: 2}}}
	r, log := poolRunner(t, c)
	r.FormWait = 700 * time.Millisecond
	// Only the solo server answers; the pooled one never does.
	answerFor(t, log, func(line string) bool { return strings.Contains(line, "--solo") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Once(ctx)
	var ring string
	wait(t, func() bool {
		rep, ok := c.find(func(r Report) bool { return r.RingAddress != nil && *r.RingAddress != "" })
		if ok {
			ring = *rep.RingAddress
		}
		return ok
	})
	endpoint := c.last().Endpoint
	c.setRing([]string{ring, "127.0.0.1:9"})
	wait(t, func() bool {
		_, ok := c.find(func(r Report) bool { return strings.Contains(r.Detail, "Could not form the pool") })
		return ok
	})
	if !strings.HasPrefix(body(t, endpoint+"/models"), "port ") {
		t.Fatal("the model should still answer on this Mac")
	}
	if _, ok := c.find(func(r Report) bool { return r.State == "failed" }); ok {
		t.Fatal("a pool that does not form is not a failure")
	}
}

func TestPoolMemberJoinsWhenTheRingCompletesAndRejoinsAfterALoss(t *testing.T) {
	c := &fakeCoord{tasks: []Task{{ID: "rinf_m", ModelID: "big", PoolID: "pool_1", PoolRank: 1, PoolSize: 2}}}
	r, log := poolRunner(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Once(ctx)
	var mine string
	wait(t, func() bool {
		rep, ok := c.find(func(r Report) bool { return r.RingAddress != nil && *r.RingAddress != "" })
		if ok {
			mine = *rep.RingAddress
		}
		return ok
	})
	if logHas(log, "--rank 1") {
		t.Fatal("it must not start its share before the ring is complete")
	}
	c.setRing([]string{"127.0.0.1:9", mine})
	wait(t, func() bool { return logHas(log, "--rank 1 --hosts 127.0.0.1:9,"+mine) })
	wait(t, func() bool { return c.last().State == "ready" && strings.Contains(c.last().Detail, "Sharing") })

	// The ring changes (the Mac that answers re-formed it): stop, leave, and offer a new address.
	c.setRing(nil)
	wait(t, func() bool {
		_, ok := c.find(func(r Report) bool { return strings.Contains(r.Detail, "Left the pool") })
		return ok
	})
	wait(t, func() bool {
		rep, ok := c.find(func(r Report) bool { return r.RingAddress != nil && *r.RingAddress != "" && *r.RingAddress != mine })
		_ = rep
		return ok
	})
}
