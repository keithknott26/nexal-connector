package discovery

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"nexal/connector/internal/client"
)

// WAN discovery is a rendezvous read, not a broadcast. The coordinator already is
// the rendezvous server, so this file is a jittered poll of GET /api/peers plus
// one piece of judgement: what to believe when the coordinator cannot be reached.
//
// The rule is that unavailability must never widen anything and must never empty
// the allowlist underneath a transfer that is already running. So a failed poll
// keeps the last known set and marks it stale with the timestamp it was observed
// at. Nothing here guesses, and nothing here fails open: before the first
// successful poll the set is empty, which means no peer is authorized at all.

const (
	// defaultPollInterval is well inside the coordinator's lease window so a peer
	// appearing or being revoked is noticed within a lease, without turning the
	// peer list into a heartbeat.
	defaultPollInterval = 30 * time.Second
	// defaultStaleAfter is when a retained set starts being reported as stale.
	defaultStaleAfter = 90 * time.Second
	// jitterFraction spreads polls so a fleet restarting together does not
	// arrive at the coordinator in lockstep.
	jitterFraction = 0.2
)

// Capabilities mirrors the coordinator's self-reported capability block. The
// name keeps the disclaimer attached to the data: nothing attests these values.
type Capabilities = client.PeerCapabilities

// AddressClaim is one address the coordinator holds for a peer.
type AddressClaim struct {
	Kind    string // "lan" or "wan".
	Address string
	Port    uint16
}

// AuthorizedPeer is a peer the coordinator listed. Being in this list is what
// Peer.Authorized means, and it is the only thing that may source AllowedPeers.
type AuthorizedPeer struct {
	HostID                   string
	Name                     string
	Fingerprint              string
	Addresses                []AddressClaim
	SelfReportedCapabilities Capabilities
}

// Snapshot is an immutable view of the last authorized set, with the honesty
// fields a caller needs to decide whether to act on it.
type Snapshot struct {
	Peers []AuthorizedPeer
	// ObservedAt is when this set was actually read from the coordinator. Zero
	// means never: no poll has ever succeeded.
	ObservedAt time.Time
	// Stale is true when the last poll failed or the set is older than
	// StaleAfter. A stale set is still the best authorization we have; it is
	// reported, not discarded.
	Stale bool
	// Expired is true when MaxStaleness was configured and exceeded, in which
	// case Peers is empty. See Rendezvous.MaxStaleness for the tradeoff.
	Expired bool
	// Age is ObservedAt's distance from now, zero if never observed.
	Age time.Duration
	// LastError is a fixed, already-scrubbed message from the client package. It
	// never contains a URL, a token or coordinator response text.
	LastError string
	// Failures counts consecutive failed polls.
	Failures int
}

// Ready reports whether the coordinator has ever answered. Callers should show
// "not yet known" rather than "no peers" when this is false: an empty list and an
// unanswered question look identical in a UI and are not the same fact.
func (s Snapshot) Ready() bool { return !s.ObservedAt.IsZero() }

// PeerSource is the coordinator read. It exists so the poller can be tested
// without a network and so nothing in this package needs a second HTTP client:
// internal/client already refuses redirects, sends no proxy credentials, bounds
// the response and scrubs transport errors.
type PeerSource interface {
	Peers(ctx context.Context) (client.PeerDirectory, error)
}

// Rendezvous polls a PeerSource and holds the last known authorized set.
type Rendezvous struct {
	source PeerSource
	// Interval is the base poll period; each wait is jittered around it.
	interval time.Duration
	// staleAfter is the age at which a retained set is reported stale.
	staleAfter time.Duration
	// maxStaleness, when non-zero, drops the retained set once exceeded.
	//
	// The tradeoff is real in both directions and is deliberately left to the
	// caller. Retaining forever means a revocation issued while the coordinator
	// is unreachable cannot reach this host, so the allowlist outlives the
	// decision to shrink it. Dropping means a coordinator outage tears down peer
	// access on healthy machines, which is the "empty the allowlist mid-transfer"
	// failure this design exists to avoid. The default is zero — retain and
	// report — because pool's peerPolicy independently re-checks enrolment and
	// revocation on every handshake, so a retained fingerprint is not by itself a
	// grant.
	maxStaleness time.Duration
	now          func() time.Time
	// rng is a private generator so tests are deterministic and so this poller
	// does not perturb the global source.
	rng *rand.Rand

	mu       sync.Mutex
	peers    []AuthorizedPeer
	observed time.Time
	lastErr  string
	failures int
}

// RendezvousOptions configures the poller. Zero values take documented defaults.
type RendezvousOptions struct {
	Source       PeerSource
	Sharing      SharingPolicy
	Interval     time.Duration
	StaleAfter   time.Duration
	MaxStaleness time.Duration
	Clock        func() time.Time
	Seed         [32]byte
}

// NewRendezvous fails closed on a shut gate, the same way Listen does.
func NewRendezvous(o RendezvousOptions) (*Rendezvous, error) {
	if !o.Sharing.WANRendezvous {
		return nil, ErrSharingDisabled
	}
	if o.Source == nil {
		return nil, ErrInvalid
	}
	r := &Rendezvous{
		source: o.Source, interval: o.Interval, staleAfter: o.StaleAfter,
		maxStaleness: o.MaxStaleness, now: o.Clock,
		rng: rand.New(rand.NewChaCha8(o.Seed)),
	}
	if r.interval <= 0 {
		r.interval = defaultPollInterval
	}
	if r.staleAfter <= 0 {
		r.staleAfter = defaultStaleAfter
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.maxStaleness < 0 || (r.maxStaleness > 0 && r.maxStaleness < r.staleAfter) {
		return nil, ErrInvalid
	}
	return r, nil
}

// Refresh performs exactly one poll. On failure the previous set is retained and
// the error is recorded; the returned error is the caller's to log, not a signal
// to clear anything.
func (r *Rendezvous) Refresh(ctx context.Context) error {
	directory, err := r.source.Peers(ctx)
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.failures++
		r.lastErr = err.Error()
		return err
	}
	peers := make([]AuthorizedPeer, 0, len(directory.Peers))
	for _, p := range directory.Peers {
		if !ValidFingerprint(p.Fingerprint) || !ValidHostID(p.HostID) {
			// A row we cannot pin is a row we cannot authenticate, so it is not
			// carried forward as authorized. Skipping it is the fail-closed
			// direction; rejecting the whole response would let one bad row
			// delete a working allowlist.
			continue
		}
		peer := AuthorizedPeer{
			HostID: p.HostID, Name: p.Name, Fingerprint: p.Fingerprint,
			SelfReportedCapabilities: p.SelfReportedCapabilities,
		}
		for _, a := range p.Addresses {
			peer.Addresses = append(peer.Addresses, AddressClaim{Kind: a.Kind, Address: a.Address, Port: a.Port})
		}
		peers = append(peers, peer)
	}
	// A successful poll returning no peers does empty the set: that is the
	// coordinator's answer, not an outage, and honouring it is how revocation and
	// removal actually take effect.
	r.peers, r.observed, r.lastErr, r.failures = peers, now, "", 0
	return nil
}

// Snapshot returns the current authorized set with its staleness.
func (r *Rendezvous) Snapshot() Snapshot {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{
		ObservedAt: r.observed, LastError: r.lastErr, Failures: r.failures,
		Peers: append([]AuthorizedPeer(nil), r.peers...),
	}
	if !r.observed.IsZero() {
		s.Age = now.Sub(r.observed)
		if s.Age < 0 {
			s.Age = 0 // A clock that moved backwards is not freshness.
		}
		s.Stale = s.Age > r.staleAfter
	}
	if r.failures > 0 {
		s.Stale = true
	}
	if r.maxStaleness > 0 && !r.observed.IsZero() && s.Age > r.maxStaleness {
		s.Expired, s.Peers = true, nil
	}
	return s
}

// AllowedPeers is the convenience path from the coordinator's set to the exact
// fingerprint allowlist pool.PeerOptions wants. It deliberately goes through
// Merge with no mDNS candidates at all, so that the only supported way to build
// an allowlist in this package is one that cannot see multicast input.
func (r *Rendezvous) AllowedPeers() []string {
	return AllowedPeers(Merge(r.Snapshot(), nil, LinkView{}, r.now()))
}

// Run polls until ctx is done. The first poll happens immediately so a freshly
// started agent does not wait a full interval to learn who its peers are.
func (r *Rendezvous) Run(ctx context.Context) {
	for {
		if err := r.Refresh(ctx); err != nil && errors.Is(err, context.Canceled) {
			return
		}
		timer := time.NewTimer(r.wait())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// wait returns the next poll delay, jittered by ±jitterFraction. On repeated
// failure it backs off multiplicatively up to eight intervals, because hammering
// an unreachable coordinator is how one outage becomes a thundering herd.
func (r *Rendezvous) wait() time.Duration {
	r.mu.Lock()
	failures := r.failures
	r.mu.Unlock()
	backoff := r.interval
	for range min(failures, 3) {
		backoff *= 2
	}
	span := float64(backoff) * jitterFraction
	// rng is not cryptographic and does not need to be: this only decorrelates
	// polls. Nothing about security depends on the value.
	delta := (r.rng.Float64()*2 - 1) * span
	wait := time.Duration(float64(backoff) + delta)
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}
