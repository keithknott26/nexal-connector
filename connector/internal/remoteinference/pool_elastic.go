package remoteinference

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// An elastic pool. The Mac that answers (rank 0) starts the whole model on itself first, so it answers at
// once. The other members get ready in the background. When every member is ready the pool moves the
// model onto the ring so they share the load, and the address clients use does not change. When a member
// is lost the pool says so and goes back to serving from the Mac that answers, until a member returns.
//
// The ring itself cannot change size while it runs, so "joining" and "leaving" mean starting a new server
// on the new set of Macs and moving the one address over to it.

// poolStatus is what this Mac last told the coordinator, resent as a heartbeat so a pool member that has
// gone quiet can be told apart from one that is only idle.
type poolStatus struct {
	state, step, detail, endpoint string
	progress                      float64
	ring                          *string
}

func (r *Runner) poolReport(ctx context.Context, t Task, s poolStatus) {
	p := s.progress
	_ = r.Coord.ReportRemoteInference(ctx, r.HostID, Report{TaskID: t.ID, State: s.state, Step: s.step, Detail: clean(s.detail, 280), Endpoint: s.endpoint, RingAddress: s.ring, Progress: &p})
}

// proc is one helper process: a model server this runner started.
type proc struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	tail   *lineTail
}

func (r *Runner) startProc(ctx context.Context, args ...string) (*proc, error) {
	pctx, cancel := context.WithCancel(ctx)
	cmd := r.command(pctx, args...)
	tail := &lineTail{}
	cmd.Stdout, cmd.Stderr = tail, tail
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	p := &proc{cancel: cancel, done: make(chan struct{}), tail: tail}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop asks the helper to end, which takes its model server down with it, and waits for that.
func (p *proc) stop() {
	if p == nil {
		return
	}
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
	}
}

func (p *proc) exit() string { return poolExit(p.tail.String(), p.err) }

// fetch reads this request from the coordinator now, so a pool reacts within seconds instead of at the
// next regular poll. ok is false once it was cancelled or removed.
func (r *Runner) fetch(ctx context.Context, id string) (Task, bool) {
	tasks, err := r.Coord.RemoteInferenceTasks(ctx, r.HostID)
	if err != nil {
		return r.current(id) // a coordinator hiccup is not a cancellation
	}
	snapshot := map[string]Task{}
	for _, t := range tasks {
		snapshot[t.ID] = t
	}
	r.mu.Lock()
	r.latest = snapshot
	r.mu.Unlock()
	t, ok := snapshot[id]
	return t, ok
}

func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// healthy asks the server at hostPort for one token. A server whose ring is broken still lists its models,
// but it cannot generate.
func healthy(ctx context.Context, hostPort string, timeout time.Duration) error {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := `{"model":"default_model","max_tokens":1,"temperature":0,"stream":false,"chat_template_kwargs":{"enable_thinking":false},"messages":[{"role":"user","content":"Hi"}]}`
	req, _ := http.NewRequestWithContext(c, http.MethodPost, "http://"+hostPort+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ringHas reports whether the coordinator's ring is complete and has this member's address at its rank.
func ringHas(ring []string, rank int, mine string) bool {
	return mine != "" && rank < len(ring) && ring[rank] == mine
}

func sameRing(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tailText keeps the end of s, where the cause usually is.
func tailText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "..." + string(r[len(r)-n:])
}

// pool runs this Mac's part in a pool until the request ends.
func (r *Runner) pool(ctx context.Context, t Task, addr string) {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	r.serving[t.ID] = &serve{cancel: cancel}
	r.mu.Unlock()
	defer r.drop(t.ID)
	if t.PoolRank == 0 {
		r.poolLeader(pctx, t, addr)
	} else {
		r.poolMember(pctx, t, addr)
	}
}

// leader is the Mac that answers for the pool.
type leader struct {
	r        *Runner
	t        Task
	addr     string
	fwd      *Forwarder
	endpoint string
	status   poolStatus
	lastBeat time.Time

	solo, pooled *proc
	pooledPort   int
	ringAddr     string // what this Mac has published; "" when it is not offering to form a pool
	ringPort     int
	nextTry      time.Time
	failures     int
	lastProbe    time.Time
	probeFails   int
	pooledAt     time.Time // when the current pool started answering
	wake         wakeWatch
	ringGone     persist
}

func (r *Runner) poolLeader(ctx context.Context, t Task, addr string) {
	apiPort, err := r.freePort(addr)
	if err != nil {
		r.fail(ctx, t, "pool", err.Error())
		return
	}
	defer r.release(apiPort)
	fwd, err := NewForwarder(net.JoinHostPort(addr, strconv.Itoa(apiPort)), net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)))
	if err != nil {
		r.fail(ctx, t, "serve", fmt.Sprintf("Cannot answer on %s:%d: %v", addr, apiPort, err))
		return
	}
	defer fwd.Close()
	l := &leader{r: r, t: t, addr: addr, fwd: fwd, endpoint: fmt.Sprintf("http://%s:%d/v1", addr, apiPort)}
	defer l.shutdown()
	if !l.startSolo(ctx, "Starting the model on this Mac") {
		return
	}
	tick := time.NewTicker(r.PoolPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if l.wake.tick(time.Now(), r.PoolPoll, r.WakeGrace) {
			r.Logger.Info("woke from sleep; giving the pool time to settle", "model", t.ModelID)
			l.probeFails, l.lastProbe = 0, time.Now()
			l.ringGone.ok()
		}
		cur, ok := r.fetch(ctx, t.ID)
		if !ok {
			return // cancelled
		}
		if l.pooled != nil {
			if !l.watch(ctx, cur) {
				return
			}
		} else {
			if !l.solo.alive() {
				r.fail(ctx, t, "serve", l.solo.exit())
				return
			}
			if cur.PoolSize > 1 && !time.Now().Before(l.nextTry) {
				if l.ringAddr == "" {
					l.offer()
				} else if ringHas(cur.PoolRing, 0, l.ringAddr) {
					l.form(ctx, cur.PoolRing)
				}
			}
		}
		l.beat(false)
	}
}

func (l *leader) shutdown() {
	l.fwd.SetUpstream("", "The model was stopped.")
	l.pooled.stop()
	l.solo.stop()
	if l.ringPort != 0 {
		l.r.release(l.ringPort)
	}
}

func (l *leader) say(s poolStatus) {
	if s.ring == nil {
		s.ring = l.status.ring
	}
	l.status = s
	l.lastBeat = time.Now()
	l.r.poolReport(context.Background(), l.t, s)
}

// beat resends the current status every Heartbeat, or at once when force is set.
func (l *leader) beat(force bool) {
	if force || time.Since(l.lastBeat) >= l.r.Heartbeat {
		l.lastBeat = time.Now()
		l.r.poolReport(context.Background(), l.t, l.status)
	}
}

func (l *leader) soloText() string {
	if l.t.PoolSize > 1 {
		return "Serving on this Mac alone. The other Macs will share the load when they are ready."
	}
	return "Serving " + l.t.ModelID
}

// startSolo runs the whole model on this Mac and points the address at it. It reports failure itself.
func (l *leader) startSolo(ctx context.Context, why string) bool {
	r, t := l.r, l.t
	port, err := freeLoopbackPort()
	if err != nil {
		r.fail(ctx, t, "serve", err.Error())
		return false
	}
	p, err := r.startProc(ctx, "pool-serve", t.ModelID, "--solo", "--api-host", "127.0.0.1", "--api-port", strconv.Itoa(port))
	if err != nil {
		r.fail(ctx, t, "serve", err.Error())
		return false
	}
	hostPort := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	started := time.Now()
	deadline := started.Add(r.ReadyAfter)
	l.say(poolStatus{state: "running", step: "serve", detail: why, progress: 0.9})
	for {
		select {
		case <-ctx.Done():
			p.stop()
			return false
		case <-p.done:
			r.fail(ctx, t, "serve", p.exit())
			return false
		case <-time.After(2 * time.Second):
		}
		el := time.Since(started)
		if time.Since(l.lastBeat) >= 5*time.Second {
			// Loading takes a while and has no real measure: creep towards 99% so the bar shows it is alive.
			l.say(poolStatus{state: "running", step: "serve", detail: fmt.Sprintf("%s (%ds)", why, int(el.Seconds())), progress: 0.9 + 0.09*(1-math.Exp(-el.Seconds()/90))})
		}
		if ok, _ := up(ctx, "http://"+hostPort+"/v1"); ok && healthy(ctx, hostPort, 90*time.Second) == nil {
			break
		}
		if time.Now().After(deadline) {
			p.stop()
			r.fail(ctx, t, "serve", "The model did not start answering. Server output: "+tailText(lastLines(p.tail.String(), 4), 200))
			return false
		}
	}
	l.solo = p
	l.fwd.SetUpstream(hostPort, "")
	l.say(poolStatus{state: "ready", step: "serve", detail: l.soloText(), endpoint: l.endpoint, progress: 1})
	return true
}

// offer publishes this Mac's ring address: the coordinator hands the ring out once every member has one.
func (l *leader) offer() {
	port, err := l.r.freePort(l.addr)
	if err != nil {
		return
	}
	l.ringPort, l.ringAddr = port, net.JoinHostPort(l.addr, strconv.Itoa(port))
	ring := l.ringAddr
	s := l.status
	s.ring = &ring
	l.say(s)
}

// leaveRing takes this Mac out of the ring and frees its port.
func (l *leader) leaveRing() {
	empty := ""
	if l.ringPort != 0 {
		l.r.release(l.ringPort)
	}
	l.ringPort, l.ringAddr = 0, ""
	s := l.status
	s.ring = &empty
	l.say(s)
}

// form starts the pooled server on the ring and moves the address to it once it generates. The solo
// server keeps answering until then.
func (l *leader) form(ctx context.Context, ring []string) {
	r, t := l.r, l.t
	port, err := freeLoopbackPort()
	if err != nil {
		return
	}
	hostPort := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p, err := r.startProc(ctx, "pool-serve", t.ModelID, "--rank", "0", "--hosts", strings.Join(ring, ","), "--api-host", "127.0.0.1", "--api-port", strconv.Itoa(port))
	if err != nil {
		l.abandon("could not start: " + err.Error())
		return
	}
	started := time.Now()
	deadline := started.Add(r.FormWait)
	s := l.status
	s.detail = fmt.Sprintf("Serving on this Mac. Forming a pool of %d Macs…", t.PoolSize)
	l.say(s)
	for {
		select {
		case <-ctx.Done():
			p.stop()
			return
		case <-p.done:
			l.abandon(p.exit())
			return
		case <-time.After(r.PoolPoll):
		}
		cur, ok := r.fetch(ctx, t.ID)
		if !ok {
			p.stop()
			return
		}
		if !ringHas(cur.PoolRing, 0, l.ringAddr) {
			p.stop()
			l.abandon("a Mac left while the pool was forming")
			return
		}
		if answering, _ := up(ctx, "http://"+hostPort+"/v1"); answering && healthy(ctx, hostPort, 90*time.Second) == nil {
			old := l.solo
			l.pooled, l.pooledPort, l.solo = p, port, nil
			l.probeFails, l.lastProbe, l.pooledAt = 0, time.Now(), time.Now()
			l.ringGone.ok()
			l.fwd.SetUpstream(hostPort, "")
			old.stop() // its memory is needed by the shared model
			l.say(poolStatus{state: "ready", step: "pool", detail: fmt.Sprintf("Serving %s with %d Macs sharing the load.", t.ModelID, t.PoolSize), endpoint: l.endpoint, progress: 1, ring: l.status.ring})
			return
		}
		if time.Now().After(deadline) {
			p.stop()
			l.abandon("the pool did not start answering in time. " + tailText(lastLines(p.tail.String(), 3), 160))
			return
		}
		l.beat(false)
	}
}

// abandon gives up one attempt to form a pool: the Mac keeps serving alone and tries again later.
func (l *leader) abandon(reason string) {
	l.failures++
	l.nextTry = time.Now().Add(poolBackoff(l.failures))
	l.leaveRing()
	l.say(poolStatus{state: "ready", step: "pool", detail: "Could not form the pool (" + tailText(reason, 150) + "). Serving on this Mac alone; will try again.", endpoint: l.endpoint, progress: 1, ring: l.status.ring})
}

// watch checks a running pool and falls back to this Mac alone when a member is lost. It returns false
// when the request is over.
func (l *leader) watch(ctx context.Context, cur Task) bool {
	r := l.r
	reason := ""
	now := time.Now()
	settling := l.wake.settling(now)
	if !l.pooled.alive() {
		reason = "the pool server stopped: " + l.pooled.exit()
	} else {
		// A Mac missing from the ring for a moment (a coordinator hiccup, a lease that lapsed during sleep)
		// is not a lost member; one that stays missing is.
		if ringHas(cur.PoolRing, 0, l.ringAddr) || settling {
			l.ringGone.ok()
		} else if l.ringGone.bad(now, r.RingGrace) {
			reason = "a Mac went quiet or left"
		}
		if reason == "" {
			inflight, quiet := l.fwd.Busy()
			if inflight > 0 {
				if quiet > r.StallAfter && !settling {
					reason = "a request stopped getting answers"
				}
			} else if time.Since(l.lastProbe) >= r.ProbeEvery {
				l.lastProbe = time.Now()
				if err := healthy(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(l.pooledPort)), 30*time.Second); err != nil {
					// An HTTP error status is an answer, not a flaky network: a pool server that returns 404 or 5xx has lost its ring.
					if strings.HasPrefix(err.Error(), "HTTP ") {
						l.probeFails = probeFailLimit
					} else {
						l.probeFails++
					}
					if l.probeFails >= probeFailLimit {
						reason = "the pool stopped answering (" + tailText(err.Error(), 80) + ")"
					}
				} else {
					l.probeFails = 0
				}
			}
		}
	}
	if reason == "" {
		return true
	}
	r.Logger.Warn("pool lost a member", "model", l.t.ModelID, "reason", reason)
	l.fwd.SetUpstream("", "The pool lost a member and is restarting on one Mac. Try again in about a minute.")
	l.pooled.stop()
	l.pooled = nil
	if time.Since(l.pooledAt) >= stableAfter {
		l.failures = 0 // it ran well for a while, so this is a new problem, not a flap
	}
	l.failures++
	l.nextTry = time.Now().Add(poolBackoff(l.failures)) // time for the other Macs to get ready; longer if it keeps failing
	l.leaveRing()
	if ctx.Err() != nil {
		return false
	}
	if !l.startSolo(ctx, "The pool lost a member ("+tailText(reason, 120)+"). Restarting on this Mac alone") {
		return false
	}
	s := l.status
	s.detail = "The pool lost a member (" + tailText(reason, 120) + "), so it is serving from this Mac alone. It will share the load again when the other Mac is ready."
	l.say(s)
	return true
}

func (r *Runner) poolMember(ctx context.Context, t Task, addr string) {
	rank := t.PoolRank
	crashes := 0 // consecutive times this Mac's share of the model died on its own
	for ctx.Err() == nil {
		port, err := r.freePort(addr)
		if err != nil {
			r.fail(ctx, t, "pool", err.Error())
			return
		}
		mine := net.JoinHostPort(addr, strconv.Itoa(port))
		st := poolStatus{state: "running", step: "pool", detail: "Ready to share the load. Waiting for the Mac that answers.", progress: 0.88, ring: &mine}
		r.poolReport(ctx, t, st)
		ring, ok := r.waitForRing(ctx, t, rank, mine, st)
		if !ok {
			r.release(port)
			return
		}
		p, err := r.startProc(ctx, "pool-serve", t.ModelID, "--rank", strconv.Itoa(rank), "--hosts", strings.Join(ring, ","), "--api-host", addr)
		if err != nil {
			r.release(port)
			r.fail(ctx, t, "serve", err.Error())
			return
		}
		st = poolStatus{state: "ready", step: "serve", detail: fmt.Sprintf("Sharing %s with the pool (Mac %d of %d).", t.ModelID, rank+1, t.PoolSize), progress: 1, ring: &mine}
		r.poolReport(ctx, t, st)
		startedAt := time.Now()
		why := r.superviseRank(ctx, t, p, ring, st)
		died := !p.alive() // its server crashed or exited, as opposed to the ring changing under it
		p.stop()
		r.release(port)
		if ctx.Err() != nil {
			return
		}
		empty := ""
		r.poolReport(ctx, t, poolStatus{state: "running", step: "pool", detail: "Left the pool (" + tailText(why, 150) + "). Ready to join again.", progress: 0.88, ring: &empty})
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.rejoinDelay(&crashes, died, time.Since(startedAt))):
		}
	}
}

// rejoinDelay is how long a member that left the pool waits before offering to join again. A server that
// keeps crashing (a failed ring, a library fault) would otherwise drag the other Macs through a new attempt
// every few seconds, so each crash in a row doubles the wait, up to two minutes.
func (r *Runner) rejoinDelay(crashes *int, died bool, ranFor time.Duration) time.Duration {
	if !died {
		*crashes = 0
		return r.Rejoin
	}
	if ranFor >= stableAfter {
		*crashes = 0
	}
	*crashes++
	wait := r.Rejoin
	for i := 1; i < *crashes && wait < 2*time.Minute; i++ {
		wait *= 2
	}
	if wait > 2*time.Minute {
		wait = 2 * time.Minute
	}
	return wait
}

// waitForRing waits until the coordinator has every member's address, this Mac's at its rank.
func (r *Runner) waitForRing(ctx context.Context, t Task, rank int, mine string, st poolStatus) ([]string, bool) {
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(r.PoolPoll):
		}
		cur, ok := r.fetch(ctx, t.ID)
		if !ok {
			return nil, false
		}
		if ringHas(cur.PoolRing, rank, mine) {
			return cur.PoolRing, true
		}
		if time.Since(last) >= r.Heartbeat {
			last = time.Now()
			r.poolReport(ctx, t, st)
		}
	}
}

// superviseRank runs until this member's server stops or the ring it joined changes; it says why.
func (r *Runner) superviseRank(ctx context.Context, t Task, p *proc, ring []string, st poolStatus) string {
	last := time.Now()
	var wake wakeWatch
	var changed persist
	for {
		select {
		case <-ctx.Done():
			return "stopped"
		case <-p.done:
			return p.exit()
		case <-time.After(r.PoolPoll):
		}
		cur, ok := r.fetch(ctx, t.ID)
		if !ok {
			return "the request ended"
		}
		now := time.Now()
		if wake.tick(now, r.PoolPoll, r.WakeGrace) {
			changed.ok()
		}
		if sameRing(cur.PoolRing, ring) || wake.settling(now) {
			changed.ok()
		} else if changed.bad(now, r.RingGrace) {
			return "the pool changed"
		}
		if time.Since(last) >= r.Heartbeat {
			last = now
			r.poolReport(ctx, t, st)
		}
	}
}
