package bandwidth

import (
	"context"
	"hash/fnv"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"nexal/connector/internal/mesh"
)

// TestInterval is how often bandwidth tests run after the initial startup test.
// 30 minutes balances freshness against burning the owner's uplink.
const TestInterval = 30 * time.Minute

// StartupDelay gives the mesh time to connect before the first bandwidth test.
const StartupDelay = 30 * time.Second

// RetryInterval is the first wait before re-testing when a connected peer still has no
// measurement (the mesh was still settling at startup, or the peer was unreachable). It
// doubles per failed cycle up to TestInterval, so a peer that can never answer (for
// example one on an older build) costs a few small tests rather than one every minute,
// while a healthy mesh gets its first reading within a minute or two instead of 30.
const RetryInterval = time.Minute

// nextDelay is how long to wait before the next cycle: TestInterval when every connected
// peer has a fresh reading, otherwise RetryInterval doubled once per consecutive cycle
// that left a peer unmeasured, capped at TestInterval.
func nextDelay(unmeasured bool, retries int) time.Duration {
	if !unmeasured {
		return TestInterval
	}
	d := RetryInterval
	for i := 0; i < retries && d < TestInterval; i++ {
		d *= 2
	}
	if d > TestInterval {
		d = TestInterval
	}
	return d
}

// startupSpread is the window a host's first test is spread over, by a stable hash of its
// neXal address, so Macs that start together do not all test (and ask to be tested) at once.
const startupSpread = 90 * time.Second

// startupOffset is this host's fixed place in startupSpread. The same address always gets the
// same offset, and different addresses are spread across the window.
func startupOffset(self string) time.Duration {
	if self == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(self))
	return time.Duration(h.Sum32()%uint32(startupSpread/time.Second)) * time.Second
}

// jitter spreads d by up to +/-10% so hosts that began in step drift apart instead of
// testing at the same moment every cycle.
func jitter(d time.Duration) time.Duration {
	span := int64(d / 5)
	if span <= 0 {
		return d
	}
	return d - d/10 + time.Duration(rand.Int64N(span))
}

// Runner manages periodic bandwidth tests to all connected mesh peers.
// It stores the most recent result per peer and exposes them for the status
// API and the coordinator report.
type Runner struct {
	mu      sync.RWMutex
	results map[string]Result // keyed by peer ID
	server  *Server
	logger  *slog.Logger
	// observedMax tracks the highest bandwidth seen across all peers and all
	// tests in this session, so the UI can set a gauge max that reflects the
	// real link speed rather than a hardcoded value.
	observedMax float64

	// On-demand port state (ondemand.go): the run context and snapshot source, the
	// coordinator call that asks a peer to open its port, and when we last opened ours.
	ctx      context.Context
	provider mesh.Provider
	opener   func(context.Context, string) error
	openerID func(context.Context, string) (string, error)
}

// NewRunner creates a bandwidth test runner.
func NewRunner(logger *slog.Logger) *Runner {
	return &Runner{
		results: make(map[string]Result),
		server:  NewServer(),
		logger:  logger,
	}
}

// Server returns the bandwidth test server so the agent can start it when the
// tunnel address is known.
func (r *Runner) Server() *Server {
	return r.server
}

// Run starts the bandwidth test loop. It runs the server on the tunnel address
// and periodically tests all connected peers. The provider is polled for the
// current peer list each cycle.
func (r *Runner) Run(ctx context.Context, provider mesh.Provider) {
	// Wait for the mesh to settle before the first test.
	select {
	case <-ctx.Done():
		return
	case <-time.After(StartupDelay):
	}

	// The port is not held open between tests: a peer opens it on request (ondemand.go).
	r.mu.Lock()
	r.ctx, r.provider = ctx, provider
	r.mu.Unlock()
	defer r.server.Stop()

	// Spread hosts out: each waits its own fixed offset before the first test.
	select {
	case <-ctx.Done():
		return
	case <-time.After(startupOffset(provider.Snapshot().SelfTunnelAddress)):
	}

	// Run initial test, then periodically. A cycle that leaves a connected peer without a
	// reading is retried soon (see RetryInterval); the full refresh still happens every
	// TestInterval.
	r.testAllPeers(ctx, provider, false)
	lastFull := time.Now()
	retries := 0
	for {
		unmeasured := r.hasUnmeasuredPeer(provider)
		if unmeasured {
			retries++
		} else {
			retries = 0
		}
		delay := jitter(nextDelay(unmeasured, max(retries-1, 0)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		full := time.Since(lastFull) >= TestInterval
		r.testAllPeers(ctx, provider, !full)
		if full {
			lastFull = time.Now()
		}
	}
}

// runsServer reports whether a peer can answer a bandwidth test: gateways and phones run no
// bandwidth server, so testing them only logs a timeout every cycle.
func runsServer(p mesh.Peer) bool {
	n := strings.ToLower(p.Name)
	return !mesh.IsGatewayPeer(n) && !strings.Contains(n, "iphone") && !strings.Contains(n, "ipad")
}

// hasUnmeasuredPeer reports whether a connected peer with a tunnel address has no fresh
// reading.
func (r *Runner) hasUnmeasuredPeer(provider mesh.Provider) bool {
	for _, peer := range mesh.SanitizeSnapshot(provider.Snapshot()).Peers {
		if (peer.Lifecycle == mesh.LifecycleConnected || peer.Lifecycle == mesh.LifecycleDegraded) &&
			peer.TunnelAddress != "" && runsServer(peer) && r.PeerBandwidth(peer.ID) == 0 {
			return true
		}
	}
	return false
}

// testAllPeers runs a bandwidth test to every connected peer sequentially.
// Sequential avoids saturating the link with parallel tests.
func (r *Runner) testAllPeers(ctx context.Context, provider mesh.Provider, onlyMissing bool) {
	status := mesh.SanitizeSnapshot(provider.Snapshot())
	for _, peer := range status.Peers {
		if ctx.Err() != nil {
			return
		}
		if peer.Lifecycle != mesh.LifecycleConnected && peer.Lifecycle != mesh.LifecycleDegraded {
			continue
		}
		if peer.TunnelAddress == "" || !runsServer(peer) {
			continue
		}
		if onlyMissing && r.PeerBandwidth(peer.ID) > 0 {
			continue
		}

		r.logger.Debug("bandwidth test starting", "peer", peer.Name, "addr", peer.TunnelAddress)
		testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result := r.measure(testCtx, peer)
		cancel()

		// One retry on failure — transient TCP or timeout errors are common on slow links.
		if result.DownloadMbps == 0 && ctx.Err() == nil {
			r.logger.Debug("bandwidth test retrying", "peer", peer.Name)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			testCtx2, cancel2 := context.WithTimeout(ctx, 30*time.Second)
			result = r.measure(testCtx2, peer)
			cancel2()
		}

		r.mu.Lock()
		if result.DownloadMbps > 0 {
			r.results[peer.ID] = result
			if result.DownloadMbps > r.observedMax {
				r.observedMax = result.DownloadMbps
			}
			r.logger.Info("bandwidth test complete",
				"peer", peer.Name, "mbps", result.DownloadMbps,
				"elapsed", result.Elapsed.Round(time.Millisecond))
		} else {
			r.logger.Warn("bandwidth test failed after retry", "peer", peer.Name, "addr", peer.TunnelAddress, "error", result.Error)
		}
		r.mu.Unlock()

		// Brief pause between peers to let buffers drain.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// PeerBandwidth returns the most recent bandwidth result for a peer, or zero.
func (r *Runner) PeerBandwidth(peerID string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.results[peerID]
	if !ok {
		return 0
	}
	// Expire results older than 2× the test interval.
	if time.Since(res.MeasuredAt) > 2*TestInterval {
		return 0
	}
	return res.DownloadMbps
}

// Results returns all current bandwidth results.
func (r *Runner) Results() map[string]Result {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Result, len(r.results))
	cutoff := time.Now().Add(-2 * TestInterval)
	for k, v := range r.results {
		if v.MeasuredAt.After(cutoff) {
			out[k] = v
		}
	}
	return out
}

// ObservedMax returns the highest bandwidth seen in this session, suitable as
// the gauge max. Returns a sensible default if no tests have completed.
func (r *Runner) ObservedMax() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.observedMax <= 0 {
		return 50 // 50 Mbps default until a test completes
	}
	// Round up to a clean number for the gauge scale.
	return ceilToNice(r.observedMax * 1.1)
}

// ceilToNice rounds up to a nice gauge-scale number:
// 10, 15, 20, 25, 30, 50, 75, 100, 150, 200, 250, 500, 1000...
func ceilToNice(v float64) float64 {
	nice := []float64{5, 10, 15, 20, 25, 30, 50, 75, 100, 150, 200, 250, 300, 500, 750, 1000}
	for _, n := range nice {
		if v <= n {
			return n
		}
	}
	// Above 1 Gbps: round to nearest 500.
	return math.Ceil(v/500) * 500
}
