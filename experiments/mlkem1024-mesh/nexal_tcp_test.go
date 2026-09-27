package rosenpass_test

import (
	rp "cunicu.li/go-rosenpass"
	"net"
	"testing"
	"time"
)

func TestNexalTCPHandshake(t *testing.T) {
	pubA, privA, err := rp.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubB, privB, err := rp.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	a, b := rp.NewTCPConn("[::1]:0"), rp.NewTCPConn("[::1]:0")
	ha := &handshakeHandler{keys: make(chan rp.Key, 16), expired: make(chan rp.PeerID, 16)}
	hb := &handshakeHandler{keys: make(chan rp.Key, 16), expired: make(chan rp.PeerID, 16)}
	sa, err := rp.NewTCPServer(rp.Config{PublicKey: pubA, SecretKey: privA, Handlers: []rp.Handler{ha}}, a)
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	sb, err := rp.NewTCPServer(rp.Config{PublicKey: pubB, SecretKey: privB, Handlers: []rp.Handler{hb}}, b)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	ae, _ := a.LocalEndpoints()
	be, _ := b.LocalEndpoints()
	aa, _ := net.ResolveUDPAddr("udp", ae[0].String())
	ba, _ := net.ResolveUDPAddr("udp", be[0].String())
	a.AuthorizeEndpoint(ba, true)
	b.AuthorizeEndpoint(aa, true)
	if _, err = sb.AddPeer(rp.PeerConfig{PublicKey: pubA}); err != nil {
		t.Fatal(err)
	}
	if _, err = sa.AddPeer(rp.PeerConfig{PublicKey: pubB, Endpoint: ba}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		var ka, kb rp.Key
		select {
		case ka = <-ha.keys:
		case <-time.After(10 * time.Second):
			t.Fatal("TCP initiator timed out")
		}
		select {
		case kb = <-hb.keys:
		case <-time.After(10 * time.Second):
			t.Fatal("TCP responder timed out")
		}
		if ka != kb {
			t.Fatal("TCP output keys differ")
		}
	}
	a.AuthorizeEndpoint(ba, false)
	b.AuthorizeEndpoint(aa, false)
	select {
	case <-ha.expired:
	case <-time.After(10 * time.Second):
		t.Fatal("revoked transport did not expire")
	}
	// After the failed renewal is exhausted, restoring transport must recover
	// without a process restart or an explicit Run call.
	a.AuthorizeEndpoint(ba, true)
	b.AuthorizeEndpoint(aa, true)
	select {
	case <-ha.keys:
	case <-time.After(20 * time.Second):
		t.Fatal("transport did not recover after expiry")
	}

}
