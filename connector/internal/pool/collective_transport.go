package pool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The ring collective's transport. It is the same transport as the object
// endpoint in peer.go, reused rather than reimplemented: TLS 1.3 only, a
// self-signed certificate over the enrolled ed25519 device key, mutual
// authentication with the peer's exact key fingerprint pinned, the registry
// consulted on every handshake so a revocation takes effect immediately, and a
// bound on connections before the handshake. peerTLS, peerPolicy and
// boundedListener are shared with it directly, so a hardening change to the peer
// transport applies here too and cannot drift.
//
// No new network stack, no second listener model, no dependency: §29.10 rejected
// a bespoke p2p stack, and §40 keeps cross-machine work on "our own Go ring
// collective over the existing fingerprint-pinned peer transport".

const (
	// collectivePath is the only route. There is no discovery endpoint, no
	// membership endpoint and no job endpoint: membership comes from the
	// coordinator's list, and this transport moves reduction chunks only.
	collectivePath = "/v1/collective"
	// maxCollectiveSessions bounds concurrent collectives per ring. A session is
	// created by the first frame that names it, which is a remote input, so the
	// count is bounded and over-cap sessions are refused rather than queued.
	maxCollectiveSessions = 4
	// maxCollectivePendingBytes bounds undrained payload per session. Honest
	// worst case for a lock-step ring is one phase of chunks from a predecessor
	// whose consumer is slow, i.e. under 2× the vector, so this ceiling is above
	// any legitimate run and below what an enrolled-but-misbehaving peer could
	// otherwise make us hold.
	maxCollectivePendingBytes = 2 * MaxCollectiveValues * 8
	// collectiveSessionTTL reaps a session that a remote opened and nothing ever
	// joined, so an abandoned collective cannot occupy a session slot forever.
	collectiveSessionTTL = 10 * time.Minute
	// collectiveHandlers bounds simultaneous frame handlers, matching the gate in
	// NewPeerServer. Excess frames are refused with 503 rather than queued into
	// unbounded goroutines.
	collectiveHandlers = 8
)

// ringTransport owns this rank's inbox, its server and one pinned client per
// other member. Clients are keyed by fingerprint, never by rank or address, so
// rank ordering can differ between constructors without the transport caring.
type ringTransport struct {
	policy   *peerPolicy
	identity Identity
	inbox    *ringInbox
	server   *http.Server
	tls      *tls.Config
	clients  map[string]*ringClient
	ranks    map[string]int // fingerprint -> rank, for sender-rank verification

	mu     sync.Mutex
	served bool
	closed bool
}

type ringClient struct {
	client      *http.Client
	transport   *http.Transport
	policy      *peerPolicy
	endpoint    string
	fingerprint string
}

// validRingEndpoint applies the same rule as NewPeerClient: an https URL with a
// literal private or loopback IP and an explicit port, no user info, no query, no
// fragment, no path. It is duplicated rather than extracted from peer.go for the
// reason discovery's localAddress gives about pool.privateIP — a shared helper is
// a single place where one caller's convenience could loosen another caller's
// check — and because rewriting the object transport was not this change's job.
func validRingEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() == "" ||
		!privateIP(net.ParseIP(u.Hostname())) {
		return "", fmt.Errorf("%w: ring endpoint must be an https private-IP address with a port", ErrUnsafePath)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", ErrInvalid
	}
	return strings.TrimSuffix(raw, "/"), nil
}

func newRingTransport(o RingOptions, r *Ring) (*ringTransport, error) {
	allowed := make([]string, 0, len(r.members))
	ranks := map[string]int{}
	for rank, m := range r.members {
		ranks[m.Fingerprint] = rank
		if m.Fingerprint != r.self {
			allowed = append(allowed, m.Fingerprint)
		}
	}
	// The server's allowlist is the ring, and the ring is the coordinator's
	// authorized set filtered by NewRing. newPeerPolicy additionally requires
	// every one of them to be an enrolled, unrevoked contributor in the
	// registry, so an authorized-but-unenrolled fingerprint fails here rather
	// than at the first handshake.
	policy, err := newPeerPolicy(o.Registry, allowed, o.Clock)
	if err != nil {
		return nil, err
	}
	cfg, err := peerTLS(o.Identity, policy, true)
	if err != nil {
		return nil, err
	}
	t := &ringTransport{policy: policy, identity: o.Identity, ranks: ranks,
		inbox:   newRingInbox(policy.now),
		clients: make(map[string]*ringClient, len(r.members)-1)}
	for _, m := range r.members {
		if m.Fingerprint == r.self {
			continue
		}
		client, err := newRingClient(o, m)
		if err != nil {
			t.Close()
			return nil, err
		}
		t.clients[m.Fingerprint] = client
	}
	t.tls = cfg
	t.server = &http.Server{
		Handler:           t.handler(r),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    8 << 10,
		TLSConfig:         cfg,
	}
	return t, nil
}

func newRingClient(o RingOptions, m RingMember) (*ringClient, error) {
	endpoint, err := validRingEndpoint(m.Endpoint)
	if err != nil {
		return nil, err
	}
	// One policy per peer, pinning exactly that peer's fingerprint: a client must
	// refuse to speak to any machine other than the one rank it is dialing, even
	// though every ring member is authorized for the ring as a whole.
	policy, err := newPeerPolicy(o.Registry, []string{m.Fingerprint}, o.Clock)
	if err != nil {
		return nil, err
	}
	cfg, err := peerTLS(o.Identity, policy, false)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		// No environment proxy and no DNS: an explicit local endpoint only.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSClientConfig:       cfg,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       15 * time.Second,
		MaxIdleConns:          2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		MaxResponseHeaderBytes: 8 << 10, DisableCompression: true,
	}
	return &ringClient{
		client: &http.Client{Transport: tr, Timeout: 2 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnauthorized }},
		transport: tr, policy: policy, endpoint: endpoint, fingerprint: m.Fingerprint,
	}, nil
}

// authorized re-checks this host and every ring member against the registry
// before a collective runs, so a revocation between construction and use stops
// the collective rather than being noticed one handshake later.
func (t *ringTransport) authorized() error {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := t.policy.enrolled(DeviceID(t.identity.PublicKey)); err != nil {
		return err
	}
	for fingerprint := range t.clients {
		if err := t.policy.enrolled(fingerprint); err != nil {
			return err
		}
	}
	return nil
}

// handler is the inbox. Its first act is the same authorization preamble as the
// object endpoint: TLS 1.3, this host still enrolled, and the client certificate
// pinned to a ring member. Its second is to bind the frame's claimed sender rank
// to the fingerprint that was actually authenticated, so no member can post as
// another rank.
func (t *ringTransport) handler(r *Ring) http.Handler {
	gate := make(chan struct{}, collectiveHandlers)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if req.TLS == nil || req.TLS.Version != tls.VersionTLS13 ||
			t.policy.enrolled(DeviceID(t.identity.PublicKey)) != nil ||
			t.policy.verifyCertificates(req.TLS.PeerCertificates) != nil {
			peerError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			peerError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			peerError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if req.URL.RawQuery != "" || req.URL.RawPath != "" || req.URL.Path != collectivePath {
			peerError(w, http.StatusNotFound, "not_found")
			return
		}
		if req.ContentLength < collectiveHeaderBytes || req.ContentLength > maxCollectiveFrameBytes ||
			req.Header.Get("Content-Encoding") != "" {
			// Refused on the declared length, before a byte of body is read.
			peerError(w, http.StatusRequestEntityTooLarge, "invalid_size")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, req.ContentLength))
		if err != nil || int64(len(body)) != req.ContentLength {
			peerError(w, http.StatusBadRequest, "invalid_frame")
			return
		}
		frame, err := decodeCollectiveFrame(body)
		if err != nil {
			peerError(w, http.StatusBadRequest, "invalid_frame")
			return
		}
		sender, ok := ringSenderFingerprint(req.TLS.PeerCertificates)
		if !ok {
			peerError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		rank, member := t.ranks[sender]
		if !member || rank != frame.senderRank || sender == r.self {
			// A ring member posting as another rank, or as this host, is refused.
			peerError(w, http.StatusForbidden, "rank_mismatch")
			return
		}
		if frame.tenant != r.tenant {
			// Tenant scope is mandatory and is checked on every frame, not only
			// at construction: a member whose scope changed under it does not get
			// to keep reducing into this tenant's vector.
			peerError(w, http.StatusForbidden, "tenant_mismatch")
			return
		}
		if frame.step >= len(r.members)-1 {
			// A step outside the algorithm's range would allocate a mailbox slot
			// no local step will ever drain.
			peerError(w, http.StatusBadRequest, "invalid_step")
			return
		}
		switch frame.kind {
		case collectiveKindAbort:
			if frame.failedRank >= len(r.members) {
				peerError(w, http.StatusBadRequest, "invalid_frame")
				return
			}
			t.inbox.abort(frame, r.members[frame.failedRank].Fingerprint)
			w.WriteHeader(http.StatusNoContent)
		default:
			switch err := t.inbox.deliver(frame); {
			case err == nil:
				// No body: there is nothing a sender should have to parse, and
				// therefore nothing it could be tempted to trust.
				w.WriteHeader(http.StatusNoContent)
			case errors.Is(err, ErrQuota):
				peerError(w, http.StatusServiceUnavailable, "busy")
			case errors.Is(err, ErrConflict):
				peerError(w, http.StatusConflict, "duplicate_step")
			default:
				peerError(w, http.StatusBadRequest, "invalid_frame")
			}
		}
	})
}

// ringSenderFingerprint reads the authenticated peer's fingerprint from the
// certificate the handshake already verified. verifyCertificates has run by the
// time this is called, so this is a read of a checked value rather than a second
// trust decision — and it is the fingerprint, never a name or an address, that
// identifies the sender.
func ringSenderFingerprint(certs []*x509.Certificate) (string, bool) {
	if len(certs) != 1 {
		return "", false
	}
	key, ok := certs[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", false
	}
	fingerprint := DeviceID(key)
	return fingerprint, validDigest(fingerprint)
}

// Serve takes ownership of listener only after validating the bound address, the
// same rule as PeerServer.Serve: an explicit private or loopback unicast
// interface, never 0.0.0.0 or ::.
func (t *ringTransport) serve(listener net.Listener) error {
	if listener == nil {
		return ErrInvalid
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !privateIP(addr.IP) {
		return fmt.Errorf("%w: explicitly bind a private unicast interface", ErrUnsafePath)
	}
	t.mu.Lock()
	if t.served || t.closed {
		t.mu.Unlock()
		return ErrConflict
	}
	t.served = true
	t.mu.Unlock()
	bounded := &boundedListener{Listener: listener, slots: make(chan struct{}, 64)}
	return t.server.Serve(tls.NewListener(bounded, t.tls))
}

// serving reports whether serve has taken ownership of a listener. It exists so a
// caller — including a test harness — can wait for the transition instead of
// racing it: serve refuses a listener once the transport is closed, so a shutdown
// that overtakes a not-yet-started Serve would otherwise look like a conflict.
func (t *ringTransport) serving() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.served
}

func (t *ringTransport) send(ctx context.Context, timeout time.Duration, frame collectiveFrame, to string) error {
	client, ok := t.clients[to]
	if !ok {
		return ErrInvalid
	}
	body, err := encodeCollectiveFrame(frame)
	if err != nil {
		return err
	}
	step, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(step, http.MethodPost, client.endpoint+collectivePath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drain the bounded remainder so the connection can be reused, and refuse a
	// response that did not come from the pinned peer even though the handshake
	// already checked it: the check is cheap and the cost of assuming is a hang.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, peerResponseLimit))
	if resp.TLS == nil || client.policy.verifyCertificates(resp.TLS.PeerCertificates) != nil {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("pool: ring peer rejected a frame (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// broadcastAbort is best effort by design: the ranks it most needs to reach are
// the ones still waiting, and the rank it cannot reach is usually the one that
// failed. Errors are therefore not propagated — there is nothing better to do
// with them, and the local failure is already being returned to the caller.
func (t *ringTransport) broadcastAbort(ctx context.Context, frame collectiveFrame) {
	var wg sync.WaitGroup
	for fingerprint := range t.clients {
		wg.Add(1)
		go func(to string) {
			defer wg.Done()
			_ = t.send(ctx, 2*time.Second, frame, to)
		}(fingerprint)
	}
	wg.Wait()
}

func (t *ringTransport) receive(ctx context.Context, mailbox *ringSession,
	timeout time.Duration, phase uint8, step int) (collectiveFrame, error) {
	return mailbox.wait(ctx, timeout, phase, step)
}

func (t *ringTransport) shutdown(ctx context.Context) error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	for _, c := range t.clients {
		c.transport.CloseIdleConnections()
	}
	if t.server == nil {
		return nil
	}
	return t.server.Shutdown(ctx)
}

func (t *ringTransport) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	for _, c := range t.clients {
		c.transport.CloseIdleConnections()
	}
	if t.server == nil {
		return nil
	}
	return t.server.Close()
}

// ringInbox holds frames that arrived for a session, including frames that
// arrived before this rank reached the matching step. That buffering is what
// keeps the ring from deadlocking on itself: a rank posts to its successor and
// then waits on its predecessor, and neither post has to find the peer already
// waiting.
type ringInbox struct {
	now      func() time.Time
	mu       sync.Mutex
	sessions map[string]*ringSession
}

type ringSession struct {
	// created is written once before the session is published and only read
	// afterwards. Everything below mu, joined included, is guarded by it: the
	// reaper reads joined while holding the inbox lock, which would otherwise be
	// a read racing a local join.
	created time.Time

	mu      sync.Mutex
	joined  bool
	total   int
	op      ReduceOp
	pending int
	slots   map[uint32]collectiveFrame
	waiters map[uint32]chan collectiveFrame
	failure *RingFailure
	aborted chan struct{}
}

func newRingInbox(now func() time.Time) *ringInbox {
	if now == nil {
		now = time.Now
	}
	return &ringInbox{now: now, sessions: map[string]*ringSession{}}
}

func slotKey(phase uint8, step int) uint32 { return uint32(phase)<<16 | uint32(uint16(step)) }

// session returns the named session, creating it if there is room. Creation is
// reachable from a remote frame, so the session count is bounded and expired
// sessions are reaped before the bound is applied.
func (i *ringInbox) session(name string, create bool) (*ringSession, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if s, ok := i.sessions[name]; ok {
		return s, nil
	}
	if !create {
		return nil, ErrInvalid
	}
	now := i.now()
	for key, s := range i.sessions {
		// i.mu then s.mu is the only order this file ever takes the two locks in;
		// no path takes them the other way round.
		s.mu.Lock()
		idle := !s.joined && now.Sub(s.created) > collectiveSessionTTL
		s.mu.Unlock()
		if idle {
			delete(i.sessions, key)
		}
	}
	if len(i.sessions) >= maxCollectiveSessions {
		return nil, ErrQuota
	}
	s := &ringSession{created: now, slots: map[uint32]collectiveFrame{},
		waiters: map[uint32]chan collectiveFrame{}, aborted: make(chan struct{})}
	i.sessions[name] = s
	return s, nil
}

// join is the local side opening a session. It records the vector length and the
// operation this rank is running, and checks any frame that already arrived
// against them, so a length or operation disagreement between ranks surfaces as
// an error instead of a wrong answer.
func (i *ringInbox) join(name string, total int, op ReduceOp) (*ringSession, error) {
	s, err := i.session(name, true)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.joined {
		return nil, ErrConflict // One local collective per session at a time.
	}
	if s.total != 0 && s.total != total {
		return nil, fmt.Errorf("%w: ranks disagree about the vector length", ErrInvalid)
	}
	if s.op != 0 && s.op != op {
		return nil, fmt.Errorf("%w: ranks disagree about the reduction", ErrInvalid)
	}
	s.joined, s.total, s.op = true, total, op
	return s, nil
}

func (i *ringInbox) leave(name string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.sessions, name)
}

// deliver hands one chunk frame to the session, waking a waiter if one is
// already blocked on that step.
func (i *ringInbox) deliver(frame collectiveFrame) error {
	s, err := i.session(frame.session, true)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total == 0 {
		s.total, s.op = frame.total, frame.op
	}
	if frame.total != s.total || frame.op != s.op {
		return fmt.Errorf("%w: frame disagrees with the session's vector length or reduction", ErrInvalid)
	}
	key := slotKey(frame.phase, frame.step)
	if waiter, ok := s.waiters[key]; ok {
		delete(s.waiters, key)
		waiter <- frame
		return nil
	}
	if _, exists := s.slots[key]; exists {
		return ErrConflict
	}
	bytesPending := s.pending + 8*len(frame.values)
	if bytesPending > maxCollectivePendingBytes {
		return ErrQuota
	}
	s.pending = bytesPending
	s.slots[key] = frame
	return nil
}

// abort records another rank's failure report and wakes every waiter at once.
func (i *ringInbox) abort(frame collectiveFrame, failed string) {
	s, err := i.session(frame.session, false)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return
	}
	s.failure = &RingFailure{Rank: frame.failedRank, Fingerprint: failed,
		Reason: frame.reason, err: ErrAborted}
	close(s.aborted)
}

// wait blocks for one step's frame. Every exit is bounded: the frame arrives, an
// abort names a failed rank, the step timeout expires, or the caller's context is
// cancelled. There is no path that waits indefinitely, which is the requirement a
// dead rank must not be able to violate.
func (s *ringSession) wait(ctx context.Context, timeout time.Duration, phase uint8, step int) (collectiveFrame, error) {
	key := slotKey(phase, step)
	s.mu.Lock()
	if frame, ok := s.slots[key]; ok {
		delete(s.slots, key)
		s.pending -= 8 * len(frame.values)
		s.mu.Unlock()
		return frame, nil
	}
	if failure := s.failure; failure != nil {
		s.mu.Unlock()
		return collectiveFrame{}, failure
	}
	if _, exists := s.waiters[key]; exists {
		s.mu.Unlock()
		return collectiveFrame{}, ErrConflict
	}
	waiter := make(chan collectiveFrame, 1)
	s.waiters[key] = waiter
	s.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer func() {
		s.mu.Lock()
		if s.waiters[key] == waiter {
			delete(s.waiters, key)
		}
		s.mu.Unlock()
	}()
	select {
	case frame := <-waiter:
		return frame, nil
	case <-s.aborted:
		s.mu.Lock()
		failure := s.failure
		s.mu.Unlock()
		if failure == nil {
			return collectiveFrame{}, ErrAborted
		}
		return collectiveFrame{}, failure
	case <-timer.C:
		return collectiveFrame{}, ErrTimeout
	case <-ctx.Done():
		return collectiveFrame{}, ctx.Err()
	}
}
