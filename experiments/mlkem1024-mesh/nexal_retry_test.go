package rosenpass

import (
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"
)

type retryConn struct {
	fail  bool
	sends int
}

func (c *retryConn) Close() error                        { return nil }
func (c *retryConn) Open() ([]ReceiveFunc, error)        { return nil, nil }
func (c *retryConn) LocalEndpoints() ([]Endpoint, error) { return nil, nil }
func (c *retryConn) Send(_ payload, _ spk, _ Endpoint) error {
	c.sends++
	if c.fail {
		return errors.New("injected final reply timeout")
	}
	return nil
}

type retryHandler struct{ calls int }

func (h *retryHandler) HandshakeCompleted(_ pid, _ key) { h.calls++ }
func retryFixture(t *testing.T) (*Server, *peer, *initConf, *retryConn, *retryHandler, Endpoint) {
	t.Helper()
	c := &retryConn{fail: true}
	handler := &retryHandler{}
	s := &Server{logger: slog.Default(), peers: make(map[pid]*peer), conn: c, handlers: []Handler{handler}}
	p := &peer{server: s, logger: slog.Default()}
	s.peers[p.PID()] = p
	h := &responderHandshake{handshake: handshake{server: s, peer: p}}
	biscuit, err := h.storeBiscuit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.encryptAndMix(nil); err != nil {
		t.Fatal(err)
	}
	h.mix(h.sidi[:], h.sidr[:])
	auth, err := h.encryptAndMix(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		s.closed = true
		if p.rekeyTimer != nil {
			p.rekeyTimer.Stop()
		}
	})
	return s, p, &initConf{sidi: h.sidi, sidr: h.sidr, biscuit: biscuit, auth: authTag(auth)}, c, handler, (*UDPEndpoint)(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
}
func TestNexalFinalReplyRetry(t *testing.T) {
	s, p, msg, c, h, ep := retryFixture(t)
	if err := s.handle(msg, ep); err == nil {
		t.Fatal("expected injected timeout")
	}
	if h.calls != 0 {
		t.Fatal("installed before delivery")
	}
	c.fail = false
	if err := s.handle(msg, ep); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatal("did not recover")
	}
	generation := p.generation
	deadline := p.confirmedReply.deadline
	if err := s.handle(msg, ep); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 || p.generation != generation || p.confirmedReply.deadline != deadline {
		t.Fatal("duplicate reinstalled or extended session")
	}
	forged := *msg
	forged.auth[0] ^= 1
	if err := s.handle(&forged, ep); err == nil {
		t.Fatal("accepted changed MAC")
	}
}
func TestNexalFinalReplyStaleRejected(t *testing.T) {
	for _, mode := range []string{"expired", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			s, p, msg, c, h, ep := retryFixture(t)
			_ = s.handle(msg, ep)
			c.fail = false
			if mode == "expired" {
				p.confirmedReply.deadline = time.Now().Add(-time.Second)
			} else {
				p.generation++
			}
			if err := s.handle(msg, ep); err == nil {
				t.Fatal("accepted stale retry")
			}
			if h.calls != 0 {
				t.Fatal("installed stale key")
			}
		})
	}
}
