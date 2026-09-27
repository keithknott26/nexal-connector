package rosenpass

import (
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"time"
)

func (m *mockIface) InstallQuantumKey(key string, psk wgtypes.Key, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, setPSKCall{peerKey: key, psk: psk, updateOnly: len(m.calls) > 0})
	return m.err
}
func (m *mockIface) RevokeQuantum(string)            {}
func (m *mockIface) QuantumSessionReady(string) bool { return m.err == nil }
