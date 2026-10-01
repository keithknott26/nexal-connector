package rosenpass

import (
	rp "cunicu.li/go-rosenpass"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestNexalInstalledKeyEvidence(t *testing.T) {
	for _, transition := range []string{"expiry", "remove", "reconnect", "interface", "write failure", "deadline"} {
		t.Run(transition, func(t *testing.T) {
			link := newHandlerTestLink(t, nil)
			h, id := link.handlerA, link.pidB
			profile, _, _, reason := h.QuantumEvidence(id)
			require.Equal(t, ReasonExchangePending, reason)
			require.Empty(t, profile)
			link.complete(rp.Key{1})
			profile, installed, expires, reason := h.QuantumEvidence(id)
			require.Empty(t, reason)
			require.Equal(t, "nexal-mlkem1024-tcp-v2", profile)
			start, err := time.Parse(time.RFC3339Nano, installed)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339Nano, expires)
			require.NoError(t, err)
			require.Equal(t, rp.RejectAfterTime, end.Sub(start))
			switch transition {
			case "expiry":
				h.HandshakeExpired(id)
			case "remove":
				h.RemovePeer(id)
			case "reconnect":
				h.AddPeer(id, "wt0", rp.Key(link.wgKeyB))
			case "interface":
				h.SetInterface(nil)
			case "write failure":
				link.ifaceA.err = errors.New("rejected")
				h.HandshakeCompleted(id, rp.Key{2})
			case "deadline":
				h.mu.Lock()
				h.peers[id].expiresAt = time.Now().Add(-time.Second)
				h.mu.Unlock()
			}
			profile, installed, expires, reason = h.QuantumEvidence(id)
			if transition != "remove" {
				require.NotEmpty(t, reason, "missing evidence must carry a reason")
			}
			require.Empty(t, profile)
			require.Empty(t, installed)
			require.Empty(t, expires)
		})
	}
}

func TestNexalEvidenceRequiresSuccessfulWriteAndPeerBinding(t *testing.T) {
	link := newHandlerTestLink(t, nil)
	link.handlerA.SetInterface(nil)
	link.complete(rp.Key{1})
	profile, _, _, _ := link.handlerA.QuantumEvidence(link.pidB)
	require.Empty(t, profile)
	link.handlerA.SetInterface(link.ifaceA)
	link.complete(rp.Key{2})
	profile, _, _, _ = link.handlerA.QuantumEvidence(link.pidA)
	require.Empty(t, profile)
	profile, _, _, _ = link.handlerA.QuantumEvidence(link.pidB)
	require.NotEmpty(t, profile)
	link.expire()
	link.complete(rp.Key{3})
	profile, _, _, _ = link.handlerA.QuantumEvidence(link.pidB)
	require.NotEmpty(t, profile)
}
