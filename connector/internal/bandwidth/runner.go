package bandwidth

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"nexal/connector/internal/mesh"
)

// TestInterval is how often bandwidth tests run after the initial startup test.
// 30 minutes balances freshness against burning the owner's uplink.
const TestInterval = 30 * time.Minute

// StartupDelay gives the mesh time to connect before the first bandwidth test.
const StartupDelay = 30 * time.Second

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

	// Start server on our tunnel address.
	status := provider.Snapshot()
	if status.SelfTunnelAddress != "" {
		if err := r.server.Start(ctx, status.SelfTunnelAddress); err != nil {
			r.logger.Warn("bandwidth server start failed", "error", err.Error(), "addr", status.SelfTunnelAddress)
		} else {
			r.logger.Info("bandwidth server started", "addr", r.server.Addr())
		}
	}
	defer r.server.Stop()

	// Run initial test, then periodically.
	r.testAllPeers(ctx, provider)

	t := time.NewTicker(TestInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Re-check tunnel address in case it changed.
			status = provider.Snapshot()
			if status.SelfTunnelAddress != "" && r.server.Addr() == "" {
				if err := r.server.Start(ctx, status.SelfTunnelAddress); err != nil {
					r.logger.Debug("bandwidth server restart failed", "error", err.Error())
				}
			}
			r.testAllPeers(ctx, provider)
		}
	}
}

// testAllPeers runs a bandwidth test to every connected peer sequentially.
// Sequential avoids saturating the link with parallel tests.
func (r *Runner) testAllPeers(ctx context.Context, provider mesh.Provider) {
	status := mesh.SanitizeSnapshot(provider.Snapshot())
	for _, peer := range status.Peers {
		if ctx.Err() != nil {
			return
		}
		if peer.Lifecycle != mesh.LifecycleConnected && peer.Lifecycle != mesh.LifecycleDegraded {
			continue
		}
		if peer.TunnelAddress == "" {
			continue
		}

		r.logger.Debug("bandwidth test starting", "peer", peer.Name, "addr", peer.TunnelAddress)
		testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result := TestPeer(testCtx, peer.ID, peer.Name, peer.TunnelAddress)
		cancel()

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
			r.logger.Debug("bandwidth test failed or zero", "peer", peer.Name)
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
