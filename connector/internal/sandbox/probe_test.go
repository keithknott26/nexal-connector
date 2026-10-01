package sandbox

import (
	"context"
	"net"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestComputeRTT(t *testing.T) {
	st := ComputeRTT(4, []time.Duration{ms(10), ms(20), ms(10)})
	if st.Received != 3 || st.Min != ms(10) || st.Max != ms(20) {
		t.Fatalf("unexpected %+v", st)
	}
	if st.LossPct != 25 {
		t.Fatalf("loss %v", st.LossPct)
	}
	// 10, 20, 10 -> avg 13.333ms; jitter = (10+10)/2 = 10ms
	if st.Jitter != ms(10) {
		t.Fatalf("jitter %v", st.Jitter)
	}
	if empty := ComputeRTT(20, nil); empty.LossPct != 100 || empty.Received != 0 {
		t.Fatalf("empty %+v", empty)
	}
}

func TestMbps(t *testing.T) {
	if got := Mbps(125_000_000, time.Second); got != 1000 {
		t.Fatalf("got %v", got)
	}
	if Mbps(0, time.Second) != 0 || Mbps(10, 0) != 0 {
		t.Fatal("zero guards")
	}
}

func boolp(b bool) *bool { return &b }

func link(avg time.Duration, loss float64, up, down float64, relayed *bool) LinkResult {
	return LinkResult{RTT: RTTStats{Sent: 20, Received: 20, Avg: avg, LossPct: loss}, UpMbps: up, DownMbps: down, Relayed: relayed}
}

func TestEvaluateThresholds(t *testing.T) {
	th := DefaultThresholds()
	cases := []struct {
		name string
		use  Use
		r    LinkResult
		ok   bool
	}{
		{"whole-vm any link", UseWholeVM, link(ms(300), 0, 1, 1, boolp(true)), true},
		{"whole-vm no answer", UseWholeVM, LinkResult{}, false},
		{"pool-disk good", UsePoolDisk, link(ms(2), 0, 900, 940, boolp(false)), true},
		{"pool-disk rtt 10ms", UsePoolDisk, link(ms(10), 0, 900, 900, boolp(false)), false},
		{"pool-disk slow upload", UsePoolDisk, link(ms(2), 0, 199, 900, boolp(false)), false},
		{"pool-disk relayed", UsePoolDisk, link(ms(2), 0, 900, 900, boolp(true)), false},
		{"pool-disk link unknown", UsePoolDisk, link(ms(2), 0, 900, 900, nil), false},
		{"pool-disk loss 2%", UsePoolDisk, link(ms(2), 2, 900, 900, boolp(false)), false},
		{"cluster good", UseCluster, link(ms(29), 1, 60, 80, boolp(false)), true},
		{"cluster rtt 30ms", UseCluster, link(ms(30), 0, 60, 80, boolp(false)), false},
		{"cluster 49 mbit", UseCluster, link(ms(5), 0, 49, 80, boolp(false)), false},
		{"cluster relayed", UseCluster, link(ms(5), 0, 100, 100, boolp(true)), false},
		{"unknown use", Use("x"), link(ms(1), 0, 1000, 1000, boolp(false)), false},
	}
	for _, c := range cases {
		d := Evaluate(th, c.use, c.r)
		if d.OK != c.ok {
			t.Errorf("%s: OK=%v reasons=%v", c.name, d.OK, d.Reasons)
		}
		if !d.OK && len(d.Reasons) == 0 {
			t.Errorf("%s: refusal without a reason", c.name)
		}
	}
}

func TestLinkCacheTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewLinkCache(func() time.Time { return now })
	c.Put(LinkResult{PeerIP: "100.64.0.2", MeasuredAt: now})
	if _, ok := c.Get("100.64.0.2"); !ok {
		t.Fatal("fresh entry missing")
	}
	now = now.Add(ProbeCacheTTL - time.Second)
	if _, ok := c.Get("100.64.0.2"); !ok {
		t.Fatal("entry should still be fresh at 9m59s")
	}
	now = now.Add(2 * time.Second)
	if _, ok := c.Get("100.64.0.2"); ok {
		t.Fatal("entry should expire at 10 min")
	}
	c.Put(LinkResult{PeerIP: "100.64.0.3", MeasuredAt: now})
	c.Invalidate("100.64.0.3")
	if _, ok := c.Get("100.64.0.3"); ok {
		t.Fatal("invalidated entry present")
	}
}

// Loopback end-to-end: the server and prober speak the same protocol.
func TestProbeLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeProbe(ctx, ln, 200*time.Millisecond) }()
	port := ln.Addr().(*net.TCPAddr).Port
	p := &Prober{Port: port, Pings: 5, Burst: 200 * time.Millisecond}
	res, err := p.Probe(ctx, "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.RTT.Received != 5 || res.UpMbps <= 0 || res.DownMbps <= 0 {
		t.Fatalf("unexpected %+v", res)
	}
	again, err := p.Probe(ctx, "127.0.0.1", false)
	if err != nil || !again.MeasuredAt.Equal(res.MeasuredAt) {
		t.Fatal("second probe should come from cache")
	}
}
