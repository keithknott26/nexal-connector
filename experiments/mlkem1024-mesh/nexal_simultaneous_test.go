package rosenpass_test

import (
	"net"
	"sync"
	"testing"
	"time"

	rp "cunicu.li/go-rosenpass"
)

type lastKeyHandler struct {
	mu   sync.Mutex
	keys []rp.Key
	at   time.Time
}

func (h *lastKeyHandler) HandshakeCompleted(_ rp.PeerID, k rp.Key) {
	h.mu.Lock()
	h.keys = append(h.keys, k)
	h.at = time.Now()
	h.mu.Unlock()
}
func (h *lastKeyHandler) snap() ([]rp.Key, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]rp.Key(nil), h.keys...), h.at
}

// Both ends initiate at the same instant. Whatever the interleaving, the two
// ends must settle on the same final key (the one WireGuard will use).
func TestNexalSimultaneousInitiationConverges(t *testing.T) {
	const rounds = 40
	bad := 0
	for i := 0; i < rounds; i++ {
		pubA, privA, _ := rp.GenerateKeyPair()
		pubB, privB, _ := rp.GenerateKeyPair()
		a, b := rp.NewTCPConn("127.0.0.1:0"), rp.NewTCPConn("127.0.0.1:0")
		ha, hb := &lastKeyHandler{}, &lastKeyHandler{}
		sa, err := rp.NewTCPServer(rp.Config{PublicKey: pubA, SecretKey: privA, Handlers: []rp.Handler{ha}}, a)
		if err != nil {
			t.Fatal(err)
		}
		sb, err := rp.NewTCPServer(rp.Config{PublicKey: pubB, SecretKey: privB, Handlers: []rp.Handler{hb}}, b)
		if err != nil {
			t.Fatal(err)
		}
		ae, _ := a.LocalEndpoints()
		be, _ := b.LocalEndpoints()
		aa, _ := net.ResolveUDPAddr("udp", ae[0].String())
		ba, _ := net.ResolveUDPAddr("udp", be[0].String())
		a.AuthorizeEndpoint(ba, true)
		b.AuthorizeEndpoint(aa, true)
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() { defer wg.Done(); <-start; sa.AddPeer(rp.PeerConfig{PublicKey: pubB, Endpoint: ba}) }()
		go func() { defer wg.Done(); <-start; sb.AddPeer(rp.PeerConfig{PublicKey: pubA, Endpoint: aa}) }()
		close(start)
		wg.Wait()
		// Wait for quiescence: both have a key and nothing new for 1s.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			ka, ta := ha.snap()
			kb, tb := hb.snap()
			if len(ka) > 0 && len(kb) > 0 && time.Since(ta) > time.Second && time.Since(tb) > time.Second {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		ka, _ := ha.snap()
		kb, _ := hb.snap()
		if len(ka) == 0 || len(kb) == 0 || ka[len(ka)-1] != kb[len(kb)-1] {
			bad++
			t.Logf("round %d: final keys differ (A installed %d, B installed %d)", i, len(ka), len(kb))
		}
		sa.Close()
		sb.Close()
	}
	if bad > 0 {
		t.Fatalf("%d/%d rounds ended with mismatched final keys", bad, rounds)
	}
}
