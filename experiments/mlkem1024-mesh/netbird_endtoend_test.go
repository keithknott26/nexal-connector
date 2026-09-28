package rosenpass

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
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

type realGateAdapter struct{ dev *wg.Device }

func (a realGateAdapter) SetPresharedKey(k string, p wgtypes.Key, _ bool) error {
	key, err := wgtypes.ParseKey(k)
	if err != nil {
		return err
	}
	return a.dev.IpcSet(fmt.Sprintf("public_key=%s\npreshared_key=%s\n", hex.EncodeToString(key[:]), hex.EncodeToString(p[:])))
}
func (a realGateAdapter) InstallQuantumKey(k string, p wgtypes.Key, e time.Time) error {
	key, err := wgtypes.ParseKey(k)
	if err != nil {
		return err
	}
	return a.dev.InstallQuantumKey([32]byte(key), [32]byte(p), e)
}
func (a realGateAdapter) RevokeQuantum(k string) {
	key, _ := wgtypes.ParseKey(k)
	a.dev.RevokeQuantum([32]byte(key))
}
func (a realGateAdapter) QuantumSessionReady(k string) bool {
	key, _ := wgtypes.ParseKey(k)
	return a.dev.QuantumSessionReady([32]byte(key))
}

// Real ML-KEM TCP packets traverse two gated WireGuard devices at MTU 1280.
// This is rootless and exercises TCP segmentation, bootstrap, generation change,
// app traffic, expiry and recovery rather than treating an interface mock as proof.
func TestNexalEndToEndGatedMLKEM(t *testing.T) {
	oldResponder, oldInitiator, oldReject := rp.RekeyAfterTimeResponder, rp.RekeyAfterTimeInitiator, rp.RejectAfterTime
	rp.RekeyAfterTimeResponder, rp.RekeyAfterTimeInitiator, rp.RejectAfterTime = 10*time.Second, 15*time.Second, 45*time.Second
	t.Cleanup(func() {
		rp.RekeyAfterTimeResponder, rp.RekeyAfterTimeInitiator, rp.RejectAfterTime = oldResponder, oldInitiator, oldReject
	})
	binds := bindtest.NewChannelBinds()
	var devs [2]*wg.Device
	var nets [2]*netstack.Net
	var keys [2]wgtypes.Key
	ips := [2]netip.Addr{netip.MustParseAddr("1.0.0.1"), netip.MustParseAddr("1.0.0.2")}
	for i := range keys {
		var err error
		keys[i], err = wgtypes.GeneratePrivateKey()
		if err != nil {
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
	dialApp := func(timeout time.Duration) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return nets[0].DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ips[1], 8080))
	}
	if c, err := dialApp(200 * time.Millisecond); err == nil {
		c.Close()
		t.Fatal("application connection succeeded before ML-KEM")
	}
	var pubs [2]rp.PublicKey
	var privs [2]rp.SecretKey
	var handlers [2]*NetbirdHandler
	var servers [2]*rp.Server
	for i := range pubs {
		pubs[i], privs[i], err = rp.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range servers {
		remote := keys[1-i].PublicKey()
		h := NewNetbirdHandler(nil, keys[i].PublicKey())
		handlers[i] = h
		h.SetInterface(realGateAdapter{devs[i]})
		h.AddPeer(rp.PeerIDFromPublicKey(pubs[1-i]), "test", rp.Key(remote))
		c := rp.NewTCPConn(netip.AddrPortFrom(ips[i], uint16(9001+i)).String())
		ns := nets[i]
		c.Listen = func(_, addr string) (net.Listener, error) {
			ap, err := netip.ParseAddrPort(addr)
			if err != nil {
				return nil, err
			}
			return ns.ListenTCPAddrPort(ap)
		}
		c.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			ap, err := netip.ParseAddrPort(addr)
			if err != nil {
				return nil, err
			}
			return ns.DialContextTCPAddrPort(ctx, ap)
		}
		c.AuthorizeEndpoint(&net.UDPAddr{IP: ips[1-i].AsSlice(), Port: 9002 - i}, true)
		servers[i], err = rp.NewTCPServer(rp.Config{PublicKey: pubs[i], SecretKey: privs[i], Handlers: []rp.Handler{h}}, c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { servers[i].Close() })
	}
	if _, err := servers[1].AddPeer(rp.PeerConfig{PublicKey: pubs[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := servers[0].AddPeer(rp.PeerConfig{PublicKey: pubs[1], Endpoint: &net.UDPAddr{IP: ips[1].AsSlice(), Port: 9002}}); err != nil {
		t.Fatal(err)
	}
	assertApp := func() {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			c, err := dialApp(time.Second)
			if err != nil {
				continue
			}
			c.SetDeadline(time.Now().Add(time.Second))
			_, err = c.Write([]byte("protected"))
			var b [9]byte
			if err == nil {
				_, err = io.ReadFull(c, b[:])
			}
			c.Close()
			if err == nil && string(b[:]) == "protected" {
				return
			}
		}
		t.Fatal("application did not recover through fresh ML-KEM-bound sessions")
	}
	assertApp()
	for i := range handlers {
		profile, _, _ := handlers[i].QuantumEvidence(rp.PeerIDFromPublicKey(pubs[1-i]))
		if profile != "nexal-mlkem1024-tcp-v2" {
			t.Fatal("active session evidence missing")
		}
	}
	// Exercise multiple automatic renewals while verifying application traffic.
	_, previous, _ := handlers[0].QuantumEvidence(rp.PeerIDFromPublicKey(pubs[1]))
	for renewal := 0; renewal < 3; renewal++ {
		deadline := time.Now().Add(30 * time.Second)
		for {
			assertApp()
			profile, installed, _ := handlers[0].QuantumEvidence(rp.PeerIDFromPublicKey(pubs[1]))
			if profile != "" && installed != previous {
				previous = installed
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("automatic renewal did not produce fresh session evidence")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	for i := range handlers {
		handlers[i].HandshakeExpired(rp.PeerIDFromPublicKey(pubs[1-i]))
	}
	if c, err := dialApp(250 * time.Millisecond); err == nil {
		c.Close()
		t.Fatal("application admitted after expiry")
	}
	if err := servers[0].Run(); err != nil {
		t.Fatal(err)
	}
	assertApp()
}
