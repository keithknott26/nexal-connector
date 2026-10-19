package bandwidth

import (
	"context"
	"net"
	"strconv"
	"time"

	"nexal/connector/internal/mesh"
)

// On-demand bandwidth ports. A peer's port is closed except while another Mac is measuring
// against it. A test first asks the coordinator to tell the peer to open its port; the peer
// listens for one transfer and closes again as soon as that transfer completes, or after
// RequestWindow if none does. Nothing listens on the tunnel address between tests, so a
// mesh rebuild cannot leave a dead listener behind.

// RequestWindow is the longest a requested port stays open if no transfer completes.
const RequestWindow = 60 * time.Second

// openReadyTimeout bounds how long a test waits for the peer's port to start answering.
const openReadyTimeout = 10 * time.Second

// minOpenSpacing ignores repeated open requests that arrive faster than this.
const minOpenSpacing = 2 * time.Second

// SetOpener installs the call that asks a peer to open its port (the coordinator relay).
// Without one, tests dial peers directly.
func (r *Runner) SetOpener(open func(context.Context, string) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opener = open
}

// HandleRequest is the target side: a peer asked to measure against this Mac, so listen on
// the tunnel address until one transfer completes or RequestWindow passes.
func (r *Runner) HandleRequest(requestID, requesterID string) {
	r.mu.Lock()
	ctx, provider := r.ctx, r.provider
	if ctx == nil || provider == nil || time.Since(r.lastOpen) < minOpenSpacing {
		r.mu.Unlock()
		return
	}
	r.lastOpen = time.Now()
	r.mu.Unlock()
	addr := provider.Snapshot().SelfTunnelAddress
	if addr == "" {
		r.logger.Debug("bandwidth request ignored", "request", requestID, "reason", "no tunnel address")
		return
	}
	if err := r.server.StartOnce(ctx, addr, RequestWindow); err != nil {
		r.logger.Warn("bandwidth port open failed", "request", requestID, "requester", requesterID, "error", err.Error())
		return
	}
	r.logger.Info("bandwidth port opened", "request", requestID, "requester", requesterID)
}

// measure runs one test to a peer, asking it to open its port first when an opener is set.
func (r *Runner) measure(ctx context.Context, peer mesh.Peer) Result {
	r.mu.RLock()
	open := r.opener
	r.mu.RUnlock()
	if open != nil {
		if err := open(ctx, peer.ID); err != nil {
			return failedResult(peer, "open request: "+err.Error())
		}
		if err := waitForPort(ctx, peer.TunnelAddress, openReadyTimeout); err != nil {
			return failedResult(peer, "peer port did not open: "+err.Error())
		}
	}
	return TestPeer(ctx, peer.ID, peer.Name, peer.TunnelAddress)
}

func failedResult(peer mesh.Peer, why string) Result {
	return Result{
		PeerID: peer.ID, PeerName: peer.Name, TunnelAddress: peer.TunnelAddress,
		PayloadBytes: DefaultPayloadBytes, MeasuredAt: time.Now(), Error: why,
	}
}

// waitForPort dials the peer's port until it accepts a connection. The probe is closed at
// once; the server ignores it because no transfer was requested.
func waitForPort(ctx context.Context, tunnelAddr string, timeout time.Duration) error {
	addr := net.JoinHostPort(tunnelAddr, strconv.Itoa(Port))
	deadline := time.Now().Add(timeout)
	for {
		d := net.Dialer{Timeout: time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
