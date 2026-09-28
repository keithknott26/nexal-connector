package rosenpass

import (
	rp "cunicu.li/go-rosenpass"
	"errors"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"testing"
)

type reconnectIface struct {
	mockIface
	gated bool
}

func (m *reconnectIface) ConfigureQuantumControl(string, string, uint16, uint16) error {
	m.gated = true
	return nil
}
func (m *reconnectIface) SetPresharedKey(k string, p wgtypes.Key, u bool) error {
	if !m.gated {
		return errors.New("key reset before gate")
	}
	return m.mockIface.SetPresharedKey(k, p, u)
}
func TestNexalReconnectResetsStalePSK(t *testing.T) {
	for _, configured := range []bool{false, true} {
		local, _ := wgtypes.GenerateKey()
		remote, _ := wgtypes.GenerateKey()
		account, _ := wgtypes.GenerateKey()
		var psk *wgtypes.Key
		if configured {
			psk = &account
		}
		m, err := NewManager(psk, "test", local.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		iface := &reconnectIface{}
		m.SetInterface(iface)
		m.server = &mockServer{}
		m.tcpConn = rp.NewTCPConn(":0")
		defer m.tcpConn.Close()
		m.port = 1234
		pub, _, err := rp.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 1; i++ {
			if err := m.addPeer(pub, "127.0.0.1:2345", "127.0.0.1", remote.PublicKey().String()); err != nil {
				t.Fatal(err)
			}
			want, _ := DeterministicSeedKey(local.PublicKey().String(), remote.PublicKey().String())
			if configured {
				want = &account
			}
			call := iface.calls[len(iface.calls)-1]
			if call.psk != *want || !call.updateOnly {
				t.Fatal("stale PSK not replaced with bootstrap key")
			}
			pid := rp.PeerIDFromPublicKey(pub)
			m.rpWgHandler.HandshakeCompleted(pid, rp.Key(account))
			before := len(iface.calls)
			if err := m.addPeer(pub, "127.0.0.1:2345", "127.0.0.1", remote.PublicKey().String()); err != nil {
				t.Fatal(err)
			}
			if len(iface.calls) != before || !m.rpWgHandler.IsPeerInitialized(pid) {
				t.Fatal("duplicate endpoint destroyed active exchange")
			}
			if err := m.addPeer(pub, "127.0.0.1:2347", "127.0.0.1", remote.PublicKey().String()); err != nil {
				t.Fatal(err)
			}
			if iface.calls[len(iface.calls)-1].psk != *want {
				t.Fatal("changed endpoint did not reset key")
			}

		}
		iface.err = errors.New("write failed")
		if err := m.addPeer(pub, "127.0.0.1:2346", "127.0.0.1", remote.PublicKey().String()); err == nil {
			t.Fatal("failed reset must abort registration")
		}
	}
}
