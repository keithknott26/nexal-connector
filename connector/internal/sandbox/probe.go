package sandbox

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// Link probe (§7a). Before the coordinator places pool work on a peer it asks
// this Mac to measure the mesh link to that peer: 20 echo round trips for
// RTT/jitter, then a 3 s TCP burst in each direction. Results are cached for
// 10 minutes. The peer runs ServeProbe on its mesh address.
//
// RTT is measured over TCP echo rather than ICMP (no privileges needed).
// "Loss" is therefore echoes that timed out or failed, an approximation of
// packet loss that is good enough for the 1 % placement gate.

// ProbePort is the TCP port ServeProbe listens on (mesh interface only).
const ProbePort = 47931

const (
	probeModePing = 'P' // server echoes 8-byte frames
	probeModeSend = 'S' // server streams data for the burst, then closes
	probeModeRecv = 'R' // server reads until EOF and replies with the 8-byte byte count

	defaultPings = 20
	defaultBurst = 3 * time.Second
	// ProbeCacheTTL is how long a LinkResult is reused.
	ProbeCacheTTL = 10 * time.Minute
)

// RTTStats summarizes echo round trips.
type RTTStats struct {
	Sent     int
	Received int
	Min      time.Duration
	Avg      time.Duration
	Max      time.Duration
	Jitter   time.Duration // mean absolute difference of consecutive samples
	LossPct  float64
}

// ComputeRTT summarizes samples taken from `sent` attempts. It is pure.
func ComputeRTT(sent int, samples []time.Duration) RTTStats {
	st := RTTStats{Sent: sent, Received: len(samples)}
	if sent > 0 {
		st.LossPct = float64(sent-len(samples)) * 100 / float64(sent)
	}
	if len(samples) == 0 {
		return st
	}
	st.Min, st.Max = samples[0], samples[0]
	var sum time.Duration
	for _, s := range samples {
		sum += s
		if s < st.Min {
			st.Min = s
		}
		if s > st.Max {
			st.Max = s
		}
	}
	st.Avg = sum / time.Duration(len(samples))
	if len(samples) > 1 {
		var d time.Duration
		for i := 1; i < len(samples); i++ {
			diff := samples[i] - samples[i-1]
			if diff < 0 {
				diff = -diff
			}
			d += diff
		}
		st.Jitter = d / time.Duration(len(samples)-1)
	}
	return st
}

// Mbps converts bytes moved in d to megabits per second. It is pure.
func Mbps(bytes int64, d time.Duration) float64 {
	if bytes <= 0 || d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// LinkResult is one probe of one peer.
type LinkResult struct {
	PeerIP     string
	RTT        RTTStats
	UpMbps     float64 // this Mac -> peer
	DownMbps   float64 // peer -> this Mac
	Relayed    *bool   // nil when the caller does not know; set by the caller
	MeasuredAt time.Time
}

// MinMbps is the slower direction.
func (r LinkResult) MinMbps() float64 { return math.Min(r.UpMbps, r.DownMbps) }

// LinkCache holds probe results for ProbeCacheTTL.
type LinkCache struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	m   map[string]LinkResult
}

// NewLinkCache returns a cache with the standard TTL. now may be nil.
func NewLinkCache(now func() time.Time) *LinkCache {
	if now == nil {
		now = time.Now
	}
	return &LinkCache{ttl: ProbeCacheTTL, now: now, m: map[string]LinkResult{}}
}

// Get returns a fresh cached result.
func (c *LinkCache) Get(ip string) (LinkResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[ip]
	if !ok || c.now().Sub(r.MeasuredAt) >= c.ttl {
		return LinkResult{}, false
	}
	return r, true
}

// Put stores a result.
func (c *LinkCache) Put(r LinkResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[r.PeerIP] = r
}

// Invalidate drops a peer, e.g. when the link changes (relay <-> direct), so the
// next Probe re-measures.
func (c *LinkCache) Invalidate(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, ip)
}

// Prober runs link probes against peers' ServeProbe listeners.
type Prober struct {
	Port  int           // default ProbePort
	Pings int           // default 20
	Burst time.Duration // default 3 s
	Cache *LinkCache    // default: a new cache
	// Dial is replaceable for tests; default is a net.Dialer with a 3 s timeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	mu   sync.Mutex
}

func (p *Prober) defaults() (port, pings int, burst time.Duration, cache *LinkCache, dial func(context.Context, string, string) (net.Conn, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Port == 0 {
		p.Port = ProbePort
	}
	if p.Pings <= 0 {
		p.Pings = defaultPings
	}
	if p.Burst <= 0 {
		p.Burst = defaultBurst
	}
	if p.Cache == nil {
		p.Cache = NewLinkCache(nil)
	}
	if p.Dial == nil {
		d := &net.Dialer{Timeout: 3 * time.Second}
		p.Dial = d.DialContext
	}
	return p.Port, p.Pings, p.Burst, p.Cache, p.Dial
}

// Probe measures the link to peerIP (a mesh address). Unless force is set a fresh
// cached result is returned. The caller fills in Relayed from the mesh status.
func (p *Prober) Probe(ctx context.Context, peerIP string, force bool) (LinkResult, error) {
	addr, err := netip.ParseAddr(peerIP)
	if err != nil {
		return LinkResult{}, errors.New("invalid peer address")
	}
	port, pings, burst, cache, dial := p.defaults()
	ip := addr.String()
	if !force {
		if r, ok := cache.Get(ip); ok {
			return r, nil
		}
	}
	target := net.JoinHostPort(ip, fmt.Sprint(port))
	samples, err := probePings(ctx, dial, target, pings)
	if err != nil {
		return LinkResult{}, err
	}
	res := LinkResult{PeerIP: ip, RTT: ComputeRTT(pings, samples)}
	if res.RTT.Received == 0 {
		return LinkResult{}, errors.New("peer did not answer link probe")
	}
	if res.DownMbps, err = probeDown(ctx, dial, target, burst); err != nil {
		return LinkResult{}, err
	}
	if res.UpMbps, err = probeUp(ctx, dial, target, burst); err != nil {
		return LinkResult{}, err
	}
	res.MeasuredAt = time.Now()
	cache.Put(res)
	return res, nil
}

type dialFunc = func(context.Context, string, string) (net.Conn, error)

func probePings(ctx context.Context, dial dialFunc, target string, n int) ([]time.Duration, error) {
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		return nil, errors.New("cannot reach peer for link probe")
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{probeModePing}); err != nil {
		return nil, errors.New("link probe failed")
	}
	var out []time.Duration
	var frame, back [8]byte
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		binary.BigEndian.PutUint64(frame[:], uint64(i))
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		start := time.Now()
		if _, err := conn.Write(frame[:]); err != nil {
			break
		}
		if _, err := io.ReadFull(conn, back[:]); err != nil || back != frame {
			// A timeout leaves the stream unsynchronized: stop; the rest count as lost.
			break
		}
		out = append(out, time.Since(start))
		time.Sleep(50 * time.Millisecond)
	}
	return out, nil
}

func probeDown(ctx context.Context, dial dialFunc, target string, burst time.Duration) (float64, error) {
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		return 0, errors.New("cannot reach peer for link probe")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(burst + 5*time.Second))
	start := time.Now()
	if _, err := conn.Write([]byte{probeModeSend}); err != nil {
		return 0, errors.New("link probe failed")
	}
	n, _ := io.Copy(io.Discard, conn) // ends when the server closes after the burst
	return Mbps(n, time.Since(start)), nil
}

func probeUp(ctx context.Context, dial dialFunc, target string, burst time.Duration) (float64, error) {
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		return 0, errors.New("cannot reach peer for link probe")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(burst + 8*time.Second))
	start := time.Now()
	if _, err := conn.Write([]byte{probeModeRecv}); err != nil {
		return 0, errors.New("link probe failed")
	}
	buf := make([]byte, 64<<10)
	end := start.Add(burst)
	for time.Now().Before(end) {
		if _, err := conn.Write(buf); err != nil {
			return 0, errors.New("link probe failed")
		}
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	var reply [8]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return 0, errors.New("link probe failed")
	}
	// Throughput is what the peer actually received, over the time until it said so.
	return Mbps(int64(binary.BigEndian.Uint64(reply[:])), time.Since(start)), nil
}

// ServeProbe answers link probes on ln until ctx is done. Bind ln to the mesh
// interface address only. It bounds concurrency and per-connection time so it
// cannot be used to hold resources.
func ServeProbe(ctx context.Context, ln net.Listener, burst time.Duration) error {
	if burst <= 0 {
		burst = defaultBurst
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	sem := make(chan struct{}, 4)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			defer conn.Close()
			serveProbeConn(conn, burst)
		}()
	}
}

func serveProbeConn(conn net.Conn, burst time.Duration) {
	_ = conn.SetDeadline(time.Now().Add(burst + 15*time.Second))
	var mode [1]byte
	if _, err := io.ReadFull(conn, mode[:]); err != nil {
		return
	}
	switch mode[0] {
	case probeModePing:
		var f [8]byte
		for {
			if _, err := io.ReadFull(conn, f[:]); err != nil {
				return
			}
			if _, err := conn.Write(f[:]); err != nil {
				return
			}
		}
	case probeModeSend:
		buf := make([]byte, 64<<10)
		end := time.Now().Add(burst)
		for time.Now().Before(end) {
			if _, err := conn.Write(buf); err != nil {
				return
			}
		}
	case probeModeRecv:
		n, _ := io.Copy(io.Discard, conn)
		var reply [8]byte
		binary.BigEndian.PutUint64(reply[:], uint64(n))
		_, _ = conn.Write(reply[:])
	}
}

// ---- Placement thresholds (§7a table) ----

// Use is what the pool wants to do with a peer.
type Use string

const (
	UseWholeVM  Use = "whole-vm"  // run a whole VM there; only SSH/console cross the link
	UsePoolDisk Use = "pool-disk" // striped, encrypted extra volume
	UseCluster  Use = "cluster"   // multi-VM cluster across devices
)

// Thresholds are the tunable gates. Defaults come from §7a.
type Thresholds struct {
	PoolDiskMaxRTT  time.Duration
	PoolDiskMinMbps float64
	ClusterMaxRTT   time.Duration
	ClusterMinMbps  float64
	MaxLossPct      float64
}

// DefaultThresholds: pool disk RTT < 10 ms and >= 200 Mbit/s; cluster RTT < 30 ms
// and >= 50 Mbit/s; loss must not exceed 1 %.
func DefaultThresholds() Thresholds {
	return Thresholds{
		PoolDiskMaxRTT:  10 * time.Millisecond,
		PoolDiskMinMbps: 200,
		ClusterMaxRTT:   30 * time.Millisecond,
		ClusterMinMbps:  50,
		MaxLossPct:      1,
	}
}

// Decision is the verdict for one use, with every reason it failed.
type Decision struct {
	OK      bool
	Reasons []string
}

// Evaluate applies the thresholds table. It is pure.
//
//	whole-vm : any link that answers.
//	pool-disk: direct (not relayed), loss <= 1 %, RTT < 10 ms, >= 200 Mbit/s both ways.
//	cluster  : direct (not relayed), loss <= 1 %, RTT < 30 ms, >= 50 Mbit/s both ways.
//
// An unknown link type (Relayed nil) fails pool-disk and cluster: the table says
// a relayed link is never used for them, so unknown is treated as not proven direct.
func Evaluate(t Thresholds, use Use, r LinkResult) Decision {
	var why []string
	if r.RTT.Received == 0 {
		why = append(why, "peer did not answer")
	}
	switch use {
	case UseWholeVM:
	case UsePoolDisk, UseCluster:
		maxRTT, minMbps := t.PoolDiskMaxRTT, t.PoolDiskMinMbps
		if use == UseCluster {
			maxRTT, minMbps = t.ClusterMaxRTT, t.ClusterMinMbps
		}
		if r.Relayed == nil {
			why = append(why, "link type unknown (must be direct)")
		} else if *r.Relayed {
			why = append(why, "link is relayed")
		}
		if r.RTT.LossPct > t.MaxLossPct {
			why = append(why, fmt.Sprintf("loss %.1f%% > %.1f%%", r.RTT.LossPct, t.MaxLossPct))
		}
		if r.RTT.Received > 0 && r.RTT.Avg >= maxRTT {
			why = append(why, fmt.Sprintf("RTT %s >= %s", r.RTT.Avg.Round(time.Microsecond), maxRTT))
		}
		if r.MinMbps() < minMbps {
			why = append(why, fmt.Sprintf("throughput %.0f Mbit/s < %.0f", r.MinMbps(), minMbps))
		}
	default:
		why = append(why, "unknown use")
	}
	sort.Strings(why)
	return Decision{OK: len(why) == 0, Reasons: why}
}
