package rosenpass

import (
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

// blockingConn never delivers: Send parks until released.
type blockingConn struct {
	release chan struct{}
	once    sync.Once
}

func (c *blockingConn) Close() error                        { c.unblock(); return nil }
func (c *blockingConn) Open() ([]ReceiveFunc, error)        { return nil, nil }
func (c *blockingConn) LocalEndpoints() ([]Endpoint, error) { return nil, nil }
func (c *blockingConn) unblock()                            { c.once.Do(func() { close(c.release) }) }
func (c *blockingConn) Send(_ payload, _ spk, _ Endpoint) error {
	<-c.release
	return nil
}

// A peer that never answers a delivery must not hold the server state lock:
// AddPeer (called from the engine) and every timer must stay responsive.
func TestNexalSlowPeerDoesNotHoldStateLock(t *testing.T) {
	conn := &blockingConn{release: make(chan struct{})}
	t.Cleanup(conn.unblock)
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	peerPub, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{PublicKey: pub, SecretKey: priv, Conn: conn, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.unblock(); s.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := s.AddPeer(PeerConfig{PublicKey: peerPub, Endpoint: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddPeer blocked behind an undelivered InitHello")
	}
	if !s.stateMu.TryLock() {
		t.Fatal("state lock held while a delivery is in flight")
	}
	s.stateMu.Unlock()
}
