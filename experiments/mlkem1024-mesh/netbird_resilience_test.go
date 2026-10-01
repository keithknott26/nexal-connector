package rosenpass

import (
	"errors"
	"net"
	"testing"
	"time"

	rp "cunicu.li/go-rosenpass"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func resilienceManager(t *testing.T) (*Manager, *reconnectIface, *mockServer) {
	t.Helper()
	local, err := wgtypes.GenerateKey()
	require.NoError(t, err)
	m, err := NewManager(nil, "test", local.PublicKey())
	require.NoError(t, err)
	iface := &reconnectIface{}
	m.SetInterface(iface)
	server := &mockServer{}
	m.server = server
	m.tcpConn = rp.NewTCPConn(":0")
	t.Cleanup(func() { m.tcpConn.Close() })
	m.port = 1234
	return m, iface, server
}

// Peers that do not advertise the profile (phones, stock or older runtimes) are
// skipped without an error, never registered with the exchange server, never
// get a control endpoint, and report the lacks-profile reason. The gate for
// them stays closed: nothing is downgraded.
func TestNexalEligibilitySkipsPeersWithoutProfile(t *testing.T) {
	m, iface, server := resilienceManager(t)
	remote, _ := wgtypes.GenerateKey()
	pub, _, err := rp.GenerateKeyPair()
	require.NoError(t, err)
	wgKey := remote.PublicKey().String()
	for _, addr := range []string{":61518", "127.0.0.1:61518", "nexal-mlkem1024-udp-v1:61518", ""} {
		require.NoError(t, m.addPeer(pub, addr, "100.86.28.93", wgKey), addr)
	}
	require.Empty(t, server.addCalls, "skipped peer must not be registered for initiation")
	require.Empty(t, m.rpPeerIDs)
	require.Empty(t, m.controlEndpoints)
	require.False(t, iface.gated, "skipped peer must not get a control exemption")
	require.Empty(t, iface.calls, "skipped peer must not have its PSK touched")
	profile, _, _, reason := m.QuantumEvidence(wgKey)
	require.Empty(t, profile)
	require.Equal(t, ReasonPeerLacksProfile, reason)
	// A legacy key behind a bare address is skipped quietly too; the explicit
	// incompatibility error is reserved for peers that claim the profile.
	require.NoError(t, m.addPeer(make([]byte, 524160), ":61518", "100.86.28.93", wgKey))
	require.Error(t, m.addPeer(make([]byte, 524160), net.JoinHostPort(QuantumProfile, "61518"), "100.86.28.93", wgKey))

	// Upgrading the peer makes it eligible; downgrading its advertisement removes it again.
	require.NoError(t, m.addPeer(pub, net.JoinHostPort(QuantumProfile, "61518"), "100.86.28.93", wgKey))
	require.Len(t, server.addCalls, 1)
	require.Contains(t, m.rpPeerIDs, wgKey)
	_, _, _, reason = m.QuantumEvidence(wgKey)
	require.Equal(t, ReasonExchangePending, reason)
	require.NoError(t, m.addPeer(pub, ":61518", "100.86.28.93", wgKey))
	require.NotContains(t, m.rpPeerIDs, wgKey)
	require.Len(t, server.removed, 1)
	_, _, _, reason = m.QuantumEvidence(wgKey)
	require.Equal(t, ReasonPeerLacksProfile, reason)
	m.OnDisconnected(wgKey)
	_, _, _, reason = m.QuantumEvidence(wgKey)
	require.Equal(t, ReasonExchangePending, reason)
}

func TestNexalAdvertisedAddressCarriesProfile(t *testing.T) {
	m, _, _ := resilienceManager(t)
	addr := m.AdvertisedAddress()
	require.True(t, AdvertisesQuantumProfile(addr))
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	require.Equal(t, QuantumProfile, host)
	require.Equal(t, "1234", port, "older runtimes read only the port")
	require.False(t, AdvertisesQuantumProfile(":1234"))
	require.False(t, AdvertisesQuantumProfile("nexal-mlkem1024-tcp-v1:1234"))
	require.Equal(t, 1234, m.GetAddress().Port)
}

// The control delivery budget follows the current path: relayed peers get the
// longer budget, direct peers the short one, and a relay-to-direct update of
// the same identity adjusts it without destroying the exchange.
func TestNexalDeliveryBudgetFollowsPath(t *testing.T) {
	m, _, _ := resilienceManager(t)
	remote, _ := wgtypes.GenerateKey()
	pub, _, err := rp.GenerateKeyPair()
	require.NoError(t, err)
	wgKey := remote.PublicKey().String()
	relayed := true
	m.SetRelayResolver(func(key string) bool { return key == wgKey && relayed })
	require.NoError(t, m.addPeer(pub, net.JoinHostPort(QuantumProfile, "2345"), "127.0.0.1", wgKey))
	ep := m.controlEndpoints[wgKey]
	require.NotNil(t, ep)
	require.Equal(t, rp.RelayedTCPBudget, m.tcpConn.BudgetFor(ep))
	relayed = false
	require.NoError(t, m.addPeer(pub, net.JoinHostPort(QuantumProfile, "2345"), "127.0.0.1", wgKey))
	require.Equal(t, rp.DirectTCPBudget, m.tcpConn.BudgetFor(ep))
	require.Len(t, m.rpPeerIDs, 1)
	require.NoError(t, m.removePeer(wgKey))
	require.Equal(t, rp.DirectTCPBudget, m.tcpConn.BudgetFor(ep), "revoked endpoint falls back to the default budget")
	require.Greater(t, rp.RelayedTCPBudget.Dial, rp.DirectTCPBudget.Dial)
}

// Reasons follow the lifecycle: pending -> unreachable while attempts fail ->
// empty once a key is installed and the session exists -> expired after the
// lease ends. An expired lease never reports evidence again until a fresh exchange.
func TestNexalReasonFollowsLifecycle(t *testing.T) {
	link := newHandlerTestLink(t, nil)
	h, id := link.handlerA, link.pidB
	_, _, _, reason := h.QuantumEvidence(id)
	require.Equal(t, ReasonExchangePending, reason)
	h.HandshakeFailed(id, errors.New("dial tcp: i/o timeout"), 2*time.Second)
	_, _, _, reason = h.QuantumEvidence(id)
	require.Equal(t, ReasonPeerUnreachable, reason)
	link.complete(rp.Key{1})
	profile, _, _, reason := h.QuantumEvidence(id)
	require.Equal(t, QuantumProfile, profile)
	require.Empty(t, reason)
	// A failed renewal attempt while the lease is live keeps the evidence.
	h.HandshakeFailed(id, errors.New("dial tcp: i/o timeout"), 4*time.Second)
	profile, _, _, reason = h.QuantumEvidence(id)
	require.Equal(t, QuantumProfile, profile)
	require.Empty(t, reason)
	// Lease end observed locally before the library reports it: fail closed.
	h.mu.Lock()
	h.peers[id].expiresAt = time.Now().Add(-time.Millisecond)
	h.mu.Unlock()
	profile, _, _, reason = h.QuantumEvidence(id)
	require.Empty(t, profile)
	require.Equal(t, ReasonEvidenceExpired, reason)
	h.HandshakeExpired(id)
	_, _, _, reason = h.QuantumEvidence(id)
	require.Equal(t, ReasonEvidenceExpired, reason)
	h.HandshakeFailed(id, errors.New("dial tcp: i/o timeout"), 8*time.Second)
	_, _, _, reason = h.QuantumEvidence(id)
	require.Equal(t, ReasonPeerUnreachable, reason)
	// Key installed, but no WireGuard session bound to that generation yet.
	h.SetInterface(&pendingSessionIface{link.ifaceA})
	link.complete(rp.Key{2})
	_, _, _, reason = h.QuantumEvidence(id)
	require.Equal(t, ReasonSessionPending, reason)
	// Installation failure is reported, not hidden.
	h.SetInterface(link.ifaceA)
	link.ifaceA.err = errors.New("rejected")
	link.complete(rp.Key{3})
	profile, _, _, reason = h.QuantumEvidence(id)
	require.Empty(t, profile)
	require.Equal(t, ReasonKeyInstallFailed, reason)
}

type pendingSessionIface struct{ *mockIface }

func (p *pendingSessionIface) QuantumSessionReady(string) bool { return false }
