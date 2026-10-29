package rosenpass

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	rp "cunicu.li/go-rosenpass"
	"golang.zx2c4.com/wireguard/conn/bindtest"
	wg "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// A renewal must be make-before-break: while the two ends install the new key
// at different moments, the key being replaced keeps carrying application data
// (until its own lease ends). The handler used to revoke the old key right
// before installing the new one, which ended the overlap at once and denied
// data until the new WireGuard session existed.
func TestNexalRenewalKeepsDataFlowingWhilePeersDisagree(t *testing.T) {
	binds := bindtest.NewChannelBinds()
	var devs [2]*wg.Device
	var nets [2]*netstack.Net
	var keys [2]wgtypes.Key
	ips := [2]netip.Addr{netip.MustParseAddr("1.0.0.1"), netip.MustParseAddr("1.0.0.2")}
	for i := range keys {
		var err error
		if keys[i], err = wgtypes.GeneratePrivateKey(); err != nil {
			t.Fatal(err)
		}
	}
	for i := range devs {
		tun, ns, err := netstack.CreateNetTUN([]netip.Addr{ips[i]}, nil, 1280)
		if err != nil {
			t.Fatal(err)
		}
		nets[i] = ns
		d := wg.NewDevice(tun, binds[i], wg.NewLogger(wg.LogLevelError, ""))
		devs[i] = d
		t.Cleanup(d.Close)
		d.RequireQuantum()
		remote := keys[1-i].PublicKey()
		cfg := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=%s/32\nendpoint=127.0.0.1:%d\n", hex.EncodeToString(keys[i][:]), hex.EncodeToString(remote[:]), ips[1-i], 2-i)
		if err := d.IpcSet(cfg); err != nil {
			t.Fatal(err)
		}
		if err := d.Up(); err != nil {
			t.Fatal(err)
		}
		if err := d.ConfigureQuantumControl([32]byte(remote), ips[i], ips[1-i], uint16(9001+i), uint16(9002-i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range devs {
		status, err := devs[1-i].IpcGet()
		if err != nil {
			t.Fatal(err)
		}
		var port string
		for _, line := range strings.Split(status, "\n") {
			if strings.HasPrefix(line, "listen_port=") {
				port = strings.TrimPrefix(line, "listen_port=")
				break
			}
		}
		if port == "" {
			t.Fatal("missing bound endpoint")
		}
		remote := keys[1-i].PublicKey()
		if err := devs[i].IpcSet(fmt.Sprintf("public_key=%s\nendpoint=127.0.0.1:%s\n", hex.EncodeToString(remote[:]), port)); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := nets[1].ListenTCPAddrPort(netip.AddrPortFrom(ips[1], 8080))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	echo := func(timeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		c, err := nets[0].DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ips[1], 8080))
		if err != nil {
			return err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(timeout))
		if _, err := c.Write([]byte("protected")); err != nil {
			return err
		}
		var b [9]byte
		_, err = io.ReadFull(c, b[:])
		return err
	}

	var handlers [2]*NetbirdHandler
	var pids [2]rp.PeerID
	for i := range handlers {
		pub, _, err := rp.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		pids[i] = rp.PeerIDFromPublicKey(pub)
		handlers[i] = NewNetbirdHandler(nil, keys[i].PublicKey())
		handlers[i].SetInterface(realGateAdapter{devs[i]})
		handlers[i].AddPeer(pids[i], "test", rp.Key(keys[1-i].PublicKey()))
	}
	complete := func(i int, k byte) { handlers[i].HandshakeCompleted(pids[i], rp.Key{k}) }

	// First exchange: both ends install key 1 and the session binds to it.
	// WireGuard rejects a second initiation from a peer inside the same
	// ~33 ms timestamp window as a replay, so let the plain endpoint-change
	// handshake settle first (production installs follow an exchange by seconds).
	time.Sleep(300 * time.Millisecond)
	complete(0, 1)
	complete(1, 1)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if p0, _, _, _ := handlers[0].QuantumEvidence(pids[0]); p0 != "" {
			if p1, _, _, _ := handlers[1].QuantumEvidence(pids[1]); p1 != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("first ML-KEM-bound session did not become ready")
		}
		_ = echo(300 * time.Millisecond) // traffic starts the session, as in production
		time.Sleep(20 * time.Millisecond)
	}
	if err := echo(2 * time.Second); err != nil {
		t.Fatalf("application data before renewal: %v", err)
	}

	// Renewal: end 0 installs key 2 while end 1 still holds key 1, so no new
	// session can form yet. Data must keep flowing on the key-1 session.
	complete(0, 2)
	until := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(until) {
		if err := echo(time.Second); err != nil {
			t.Fatalf("application data blocked while the peers disagree on the renewed key: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The other end catches up; a session under key 2 forms and data still flows.
	complete(1, 2)
	deadline = time.Now().Add(15 * time.Second)
	for {
		if p0, _, _, _ := handlers[0].QuantumEvidence(pids[0]); p0 != "" {
			if p1, _, _, _ := handlers[1].QuantumEvidence(pids[1]); p1 != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("renewed session did not become ready")
		}
		_ = echo(300 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}
	if err := echo(2 * time.Second); err != nil {
		t.Fatalf("application data after renewal: %v", err)
	}

	// A refused install still fails closed.
	handlers[0].HandshakeExpired(pids[0])
	if err := echo(500 * time.Millisecond); err == nil {
		t.Fatal("application data admitted after expiry")
	}
}
