package device

import (
	"encoding/binary"
	"encoding/hex"
	"golang.zx2c4.com/wireguard/tun/tuntest"
	"net/netip"
	"testing"
	"time"
)

func controlPacket(src, dst netip.Addr, sp, dp uint16) []byte {
	b := make([]byte, 40)
	b[0] = 0x45
	b[9] = 6
	b[32] = 0x50
	binary.BigEndian.PutUint16(b[2:4], 40)
	copy(b[12:16], src.AsSlice())
	copy(b[16:20], dst.AsSlice())
	binary.BigEndian.PutUint16(b[20:22], sp)
	binary.BigEndian.PutUint16(b[22:24], dp)
	return b
}
func TestNexalGateRejectsStaleGenerationAndExpiry(t *testing.T) {
	local, remote := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
	d := &Device{}
	d.RequireQuantum()
	p := &Peer{device: d}
	lease := &quantumLease{generation: 2, expires: time.Now().Add(time.Minute), local: local, remote: remote, localPort: 10001, remotePort: 10002}
	p.quantum.Store(lease)
	app := tuntest.Ping(remote, local)
	for _, k := range []*Keypair{nil, {quantumGeneration: 0}, {quantumGeneration: 1}} {
		if p.quantumAllows(app, k, true) {
			t.Fatal("old key admitted application data")
		}
	}
	fresh := &Keypair{quantumGeneration: 2}
	if !p.quantumAllows(app, fresh, true) {
		t.Fatal("fresh session rejected")
	}
	control := controlPacket(local, remote, 45000, 10002)
	if !p.quantumAllows(control, nil, true) {
		t.Fatal("bootstrap control rejected")
	}
	for name, b := range map[string][]byte{"wrong port": controlPacket(local, remote, 45000, 443), "routed": controlPacket(local, netip.MustParseAddr("1.1.1.1"), 45000, 10002), "short": control[:19]} {
		if p.quantumAllows(b, nil, true) {
			t.Fatal(name)
		}
	}
	fragment := append([]byte(nil), control...)
	fragment[6] = 0x20
	if p.quantumAllows(fragment, nil, true) {
		t.Fatal("fragment bypass")
	}
	lease2 := *lease
	lease2.expires = time.Now().Add(-time.Second)
	p.quantum.Store(&lease2)
	if p.quantumAllows(app, fresh, true) {
		t.Fatal("expired session admitted app data")
	}
	if !p.quantumAllows(control, fresh, true) {
		t.Fatal("recovery channel blocked")
	}
}
func TestNexalWireGuardSessionGate(t *testing.T) {
	pair := genTestPair(t, false)
	// Create and exercise ordinary sessions first; enabling the gate must reject them.
	pair.Send(t, Ping, nil)
	var keys [2][32]byte
	for i := range pair {
		d := pair[i].dev
		d.RequireQuantum()
		for k := range d.peers.keyMap {
			keys[i] = [32]byte(k)
		}
		if err := d.ConfigureQuantumControl(keys[i], pair[i].ip, pair[1-i].ip, 10001, uint16(10002)); err != nil {
			t.Fatal(err)
		}
	}
	reject := func() {
		t.Helper()
		pair[1].tun.Outbound <- tuntest.Ping(pair[0].ip, pair[1].ip)
		select {
		case <-pair[0].tun.Inbound:
			t.Fatal("unprotected app packet arrived")
		case <-time.After(150 * time.Millisecond):
		}
	}
	reject()
	psk := [32]byte{7}
	for i := range pair {
		if err := pair[i].dev.InstallQuantumKey(keys[i], psk, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// Old-generation packets are deliberately dropped while re-keying. Wait for
	// the new session instead of assuming the first datagram is retransmitted.
	deadline := time.Now().Add(12 * time.Second)
	for !pair[0].dev.QuantumSessionReady(keys[0]) || !pair[1].dev.QuantumSessionReady(keys[1]) {
		if time.Now().After(deadline) {
			t.Fatal("fresh PSK session did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Trigger a fresh PSK-bound WireGuard handshake; old queued sessions cannot qualify.
	pair.Send(t, Ping, nil)
	pair.Send(t, Pong, nil)
	for i := range pair {
		if !pair[i].dev.QuantumSessionReady(keys[i]) {
			t.Fatal("fresh PSK session not ready")
		}
	}
	// A plain IPC write (including fallback keys) invalidates the lease immediately.
	if err := pair[1].dev.IpcSet(uapiCfg("public_key", hex.EncodeToString(keys[1][:]), "preshared_key", hex.EncodeToString(psk[:]))); err != nil {
		t.Fatal(err)
	}
	reject()
}

// Real Linux sockets exposed an empty-batch panic hidden by channel test binds.
func TestNexalRealSocketGateDropsEmptyBatch(t *testing.T) {
	pair := genTestPair(t, true)
	pair.Send(t, Ping, nil)
	for _, tp := range pair {
		tp.dev.peers.RLock()
		peers := make([]*Peer, 0, len(tp.dev.peers.keyMap))
		for _, peer := range tp.dev.peers.keyMap {
			peers = append(peers, peer)
		}
		tp.dev.peers.RUnlock()
		for _, peer := range peers {
			if err := peer.SendBuffers(nil); err != nil {
				t.Fatal(err)
			}
		}
		tp.dev.RequireQuantum()
	}
	pair[1].tun.Outbound <- tuntest.Ping(pair[0].ip, pair[1].ip)
	select {
	case <-pair[0].tun.Inbound:
		t.Fatal("unprotected packet admitted")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestNexalKeyTransitionPreservesControlOnlySession(t *testing.T) {
	pair := genTestPair(t, false)
	pair.Send(t, Ping, nil)
	d := pair[0].dev
	d.RequireQuantum()
	var key [32]byte
	for k := range d.peers.keyMap {
		key = [32]byte(k)
	}
	if err := d.ConfigureQuantumControl(key, pair[0].ip, pair[1].ip, 10001, 10002); err != nil {
		t.Fatal(err)
	}
	p := d.LookupPeer(NoisePublicKey(key))
	old := p.keypairs.Current()
	if err := d.InstallQuantumKey(key, [32]byte{42}, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if old.sendNonce.Load() >= RejectAfterMessages {
		t.Fatal("key transition killed the control session before delivery completed")
	}
	if p.quantumAllows(tuntest.Ping(pair[1].ip, pair[0].ip), old, true) {
		t.Fatal("old session admitted application traffic")
	}
	if !p.quantumAllows(controlPacket(pair[0].ip, pair[1].ip, 10001, 10002), old, true) {
		t.Fatal("old session rejected authorized control traffic")
	}
}
