package bandwidth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nexal/connector/internal/mesh"
)

// On-demand bandwidth ports. A peer's port is closed except while another Mac is measuring
// against it. A test first asks the coordinator to tell the peer to open a port; the peer
// listens for one transfer and closes again as soon as that transfer completes, or after
// RequestWindow if none does. Nothing listens on the tunnel address between tests, so a
// mesh rebuild cannot leave a dead listener behind.
//
// Each request gets its own port from the slot range (SlotPort), so tests from many Macs
// against one host (a storage gateway) run side by side instead of replacing each other.

// RequestWindow is the longest a requested port stays open if no transfer completes.
const RequestWindow = 60 * time.Second

// openReadyTimeout bounds how long a test waits for the peer's port to start answering.
const openReadyTimeout = 10 * time.Second

// maxOpenSlots caps how many requested ports one Mac holds open at once.
const maxOpenSlots = 16

// SetOpener installs the call that asks a peer to open its port (the coordinator relay).
// Without one, tests dial peers directly. This form does not learn the request id, so the
// peer is dialed on the fixed Port; SetOpenerWithID is preferred.
func (r *Runner) SetOpener(open func(context.Context, string) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opener = open
}

// SetOpenerWithID is SetOpener for a relay that returns the coordinator's request id, which
// both sides turn into the same slot port (SlotPort).
func (r *Runner) SetOpenerWithID(open func(context.Context, string) (string, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openerID = open
}

// HandleRequest is the target side: a peer asked to measure against this Mac, so listen on
// the tunnel address, on the request's slot port, until one transfer completes or
// RequestWindow passes. Every refusal is logged with its reason and the request id, so the
// two Macs' logs can be matched.
func (r *Runner) HandleRequest(requestID, requesterID string) {
	r.mu.Lock()
	ctx, provider := r.ctx, r.provider
	r.mu.Unlock()
	if ctx == nil || provider == nil {
		r.logger.Warn("bandwidth request ignored", "request", requestID, "requester", requesterID, "reason", "the agent has not finished starting")
		return
	}
	addr := provider.Snapshot().SelfTunnelAddress
	if addr == "" {
		r.logger.Warn("bandwidth request ignored", "request", requestID, "requester", requesterID, "reason", "this Mac has no neXal network address yet")
		return
	}
	if r.server.OpenSlots() >= maxOpenSlots {
		r.logger.Warn("bandwidth request ignored", "request", requestID, "requester", requesterID, "reason", "too many bandwidth ports are already open", "open", r.server.OpenSlots())
		return
	}
	port := SlotPort(requestID)
	if err := r.server.OpenSlot(ctx, addr, port, RequestWindow); err != nil {
		reason := "could not listen on the neXal address"
		switch {
		case errors.Is(err, ErrSlotBusy):
			reason = "another test holds this slot port; the requester retries with a new request"
		case errors.Is(err, syscall.EADDRNOTAVAIL):
			reason = "the neXal address is not assigned to this Mac's network interface (the mesh is rebuilding)"
		}
		r.logger.Warn("bandwidth port open failed", "request", requestID, "requester", requesterID, "addr", addr, "port", port, "reason", reason, "error", err.Error())
		return
	}
	// Peers on an older build dial the fixed Port, so offer it too when it is free.
	legacy := Port
	if err := r.server.OpenSlot(ctx, addr, legacy, RequestWindow); err != nil {
		legacy = 0
	}
	r.logger.Info("bandwidth port opened", "request", requestID, "requester", requesterID, "addr", addr, "port", port, "legacyPort", legacy, "window", RequestWindow.String())
}

// measure runs one test to a peer, asking it to open its port first when an opener is set.
func (r *Runner) measure(ctx context.Context, peer mesh.Peer) Result {
	r.mu.RLock()
	open, openID := r.opener, r.openerID
	r.mu.RUnlock()
	if open == nil && openID == nil {
		return TestPeer(ctx, peer.ID, peer.Name, peer.TunnelAddress)
	}
	requestID := ""
	var err error
	if openID != nil {
		requestID, err = openID(ctx, peer.TunnelAddress)
	} else {
		err = open(ctx, peer.TunnelAddress)
	}
	if err != nil {
		return failedResult(peer, "open request: "+describeOpenFailure(err))
	}
	ports := []int{Port}
	if requestID != "" {
		ports = []int{SlotPort(requestID), Port}
	}
	started := time.Now()
	port, err := waitForAnyPort(ctx, peer.TunnelAddress, ports, openReadyTimeout)
	if err != nil {
		kind, hint := classifyDial(err)
		why := fmt.Sprintf("peer port did not open [%s] request=%s ports=%v waited=%s: %s. %s",
			kind, requestID, ports, time.Since(started).Round(100*time.Millisecond), err.Error(), hint)
		return failedResult(peer, why)
	}
	return TestPeerOn(ctx, peer.ID, peer.Name, peer.TunnelAddress, port)
}

// describeOpenFailure explains a failed open request.
func describeOpenFailure(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "HTTP 409") {
		return msg + " (the coordinator already has a bandwidth request open for that peer; this test is retried shortly)"
	}
	if strings.Contains(msg, "HTTP 404") {
		return msg + " (the coordinator does not know a host at that neXal address; it may be offline)"
	}
	return msg
}

// classifyDial names why a dial to a peer's bandwidth port failed and what to check.
func classifyDial(err error) (kind, hint string) {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused", "The peer answered but nothing was listening, so it never opened the port. Look in the peer's agent.log for 'bandwidth request ignored' or 'bandwidth port open failed' with this request id; if neither appears the request never reached it, or it runs a build without on-demand ports."
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "no-route", "There is no route to the peer's neXal address: the mesh link to it is down."
	case errors.Is(err, context.Canceled):
		return "canceled", "The test was stopped before the peer answered."
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout", "Nothing came back: packets to the peer were dropped. If the peer logged 'bandwidth port opened' for this request id, it was listening and something on the way is dropping inbound TCP to it, most often the macOS firewall (Helpers/nexal must be set to Allow incoming connections) or a mesh access rule. If it did not, the request never reached it."
	}
	return "error", "See the error above."
}

func failedResult(peer mesh.Peer, why string) Result {
	return Result{
		PeerID: peer.ID, PeerName: peer.Name, TunnelAddress: peer.TunnelAddress,
		PayloadBytes: DefaultPayloadBytes, MeasuredAt: time.Now(), Error: why,
	}
}

// waitForPort dials the peer's fixed port until it accepts a connection.
func waitForPort(ctx context.Context, tunnelAddr string, timeout time.Duration) error {
	_, err := waitForAnyPort(ctx, tunnelAddr, []int{Port}, timeout)
	return err
}

// waitForAnyPort dials the peer's ports in order until one accepts a connection, and returns it. The
// probe is closed at once; the server ignores it because no transfer was requested. The error is the
// last dial error, so the caller can tell a timeout from a refusal.
func waitForAnyPort(ctx context.Context, tunnelAddr string, ports []int, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		for i, p := range ports {
			d := net.Dialer{Timeout: time.Second}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(tunnelAddr, strconv.Itoa(p)))
			if err == nil {
				_ = conn.Close()
				return p, nil
			}
			if i == 0 || last == nil {
				last = err // the preferred port's error says the most
			}
		}
		if time.Now().After(deadline) {
			return 0, last
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
