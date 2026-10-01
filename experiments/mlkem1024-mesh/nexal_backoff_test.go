package rosenpass

import (
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

// failingConn counts delivery attempts and fails until released.
type failingConn struct {
	mu    sync.Mutex
	fail  bool
	sends []time.Time
}

func (c *failingConn) Close() error                        { return nil }
func (c *failingConn) Open() ([]ReceiveFunc, error)        { return nil, nil }
func (c *failingConn) LocalEndpoints() ([]Endpoint, error) { return nil, nil }
func (c *failingConn) Send(_ payload, _ spk, _ Endpoint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends = append(c.sends, time.Now())
	if c.fail {
		return errors.New("dial tcp: i/o timeout")
	}
	return nil
}
func (c *failingConn) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.sends) }

type failureRecorder struct {
	mu      sync.Mutex
	delays  []time.Duration
	expired int
}

func (r *failureRecorder) HandshakeFailed(_ pid, _ error, retry time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delays = append(r.delays, retry)
}
func (r *failureRecorder) HandshakeExpired(pid) { r.mu.Lock(); defer r.mu.Unlock(); r.expired++ }
func (r *failureRecorder) snapshot() ([]time.Duration, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.delays...), r.expired
}

func withBackoff(t *testing.T, base, limit, renewal time.Duration) {
	t.Helper()
	ob, oc, orc := InitiationBackoffBase, InitiationBackoffCap, RenewalBackoffCap
	InitiationBackoffBase, InitiationBackoffCap, RenewalBackoffCap = base, limit, renewal
	t.Cleanup(func() { InitiationBackoffBase, InitiationBackoffCap, RenewalBackoffCap = ob, oc, orc })
}

func TestNexalBackoffDelayGrowsAndCaps(t *testing.T) {
	withBackoff(t, 2*time.Second, 5*time.Minute, 15*time.Second)
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		for sample := 0; sample < 20; sample++ {
			d := backoffDelay(uint(i+1), InitiationBackoffCap)
			lo, hi := time.Duration(float64(w)*0.8), time.Duration(float64(w)*1.2)
			if d < lo || d > hi {
				t.Fatalf("failure %d: delay %v outside [%v, %v]", i+1, d, lo, hi)
			}
		}
	}
	if d := backoffDelay(6, RenewalBackoffCap); d > 18*time.Second {
		t.Fatalf("renewal cap not applied: %v", d)
	}
	if d := backoffDelay(1000, InitiationBackoffCap); d > 6*time.Minute {
		t.Fatalf("overflow for large failure count: %v", d)
	}
}

func backoffServer(t *testing.T, conn Conn, handlers ...Handler) (*Server, *peer) {
	t.Helper()
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	peerPub, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{PublicKey: pub, SecretKey: priv, Conn: conn, Handlers: handlers, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.AddPeer(PeerConfig{PublicKey: peerPub, Endpoint: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}}); err != nil {
		t.Fatal(err)
	}
	s.stateMu.Lock()
	p := s.peers[PeerIDFromPublicKey(peerPub)]
	s.stateMu.Unlock()
	return s, p
}

// An unreachable peer must not be dialled every retransmission interval: the
// attempt is torn down and the next one waits for the growing backoff.
func TestNexalInitiationBackoffOnUnreachablePeer(t *testing.T) {
	withBackoff(t, 100*time.Millisecond, 2*time.Second, 2*time.Second)
	conn := &failingConn{fail: true}
	rec := &failureRecorder{}
	s, p := backoffServer(t, conn, rec)
	time.Sleep(1500 * time.Millisecond)
	delays, _ := rec.snapshot()
	// 0, ~100, ~300, ~700 ms -> four attempts in 1.5 s; retransmissions would add dozens.
	if n := conn.count(); n < 3 || n > 5 {
		t.Fatalf("expected 3-5 attempts in 1.5s, got %d", n)
	}
	for i := 1; i < len(delays); i++ {
		if delays[i] < delays[i-1] {
			t.Fatalf("backoff did not grow: %v", delays)
		}
	}
	s.stateMu.Lock()
	inflight := len(s.handshakes)
	failures := p.failures
	s.stateMu.Unlock()
	if inflight != 0 {
		t.Fatalf("failed attempts left %d handshakes in flight", inflight)
	}
	if failures < 3 {
		t.Fatalf("failure counter not advanced: %d", failures)
	}
	// Success resets the counter and schedules the regular rekey.
	conn.fail = false
	s.stateMu.Lock()
	hs := &handshake{server: s, peer: p}
	s.completeHandshake(hs, p.endpoint, time.Hour)
	reset := p.failures
	lease := p.leaseExpires
	s.stateMu.Unlock()
	if reset != 0 {
		t.Fatalf("success did not reset failures: %d", reset)
	}
	if time.Until(lease) <= 0 {
		t.Fatal("completion did not record the lease end")
	}
}

// Expiry must fire at the lease end even when every renewal attempt is torn
// down early, and the key must be reported expired exactly once per generation.
func TestNexalLeaseExpiryIndependentOfAttempts(t *testing.T) {
	withBackoff(t, 50*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond)
	old := RejectAfterTime
	RejectAfterTime = 600 * time.Millisecond
	t.Cleanup(func() { RejectAfterTime = old })
	conn := &failingConn{fail: true}
	rec := &failureRecorder{}
	s, p := backoffServer(t, conn, rec)
	s.stateMu.Lock()
	s.completeHandshake(&handshake{server: s, peer: p}, p.endpoint, 50*time.Millisecond)
	s.stateMu.Unlock()
	time.Sleep(400 * time.Millisecond)
	if _, expired := rec.snapshot(); expired != 0 {
		t.Fatal("expired before the lease end")
	}
	time.Sleep(500 * time.Millisecond)
	if _, expired := rec.snapshot(); expired != 1 {
		t.Fatalf("expected exactly one expiry at the lease end, got %d", expired)
	}
	s.stateMu.Lock()
	armed := p.expiryTimer != nil
	s.stateMu.Unlock()
	if !armed {
		t.Fatal("outage timer not re-armed after expiry")
	}
	if conn.count() < 3 {
		t.Fatalf("renewal was not retried inside the margin: %d attempts", conn.count())
	}
}
