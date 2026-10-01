package mesh

import (
	"encoding/json"
	"testing"
	"time"
)

func reasonStatus(t *testing.T, now time.Time, peer map[string]any) Status {
	t.Helper()
	input := map[string]any{"peers": map[string]any{"details": []any{peer}}, "management": map[string]any{"connected": true}, "quantumResistance": true}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return SanitizeSnapshot(translateRuntime(data, now))
}

// The runtime's machine-readable reason reaches the local status unchanged, a
// protected link carries none, and a device that never advertises the profile
// is "not covered" (unsupported) rather than a degraded link; it does not
// demote the host and still gets no sharing services.
func TestPQReasonSurfacesAndPhoneIsNotCovered(t *testing.T) {
	now := time.Now()
	connected := func(extra map[string]any) map[string]any {
		p := map[string]any{"fqdn": "m2.x", "netbirdIp": "100.1.1.2/16", "publicKey": "k", "status": "Connected", "connectionType": "P2P", "quantumResistance": true}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	for _, tc := range []struct {
		name   string
		peer   map[string]any
		pq     PQState
		reason string
	}{
		{"unreachable", connected(map[string]any{"quantumReason": "peer-unreachable"}), PQDegraded, PQReasonPeerUnreachable},
		{"expired", connected(map[string]any{"quantumReason": "evidence-expired"}), PQDegraded, PQReasonEvidenceExpired},
		{"pending without reason", connected(nil), PQDegraded, PQReasonExchangePending},
		{"stale evidence", connected(map[string]any{"quantumProfile": experimentalQuantumProfile,
			"quantumKeyInstalledAt": now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
			"quantumKeyExpiresAt":   now.Add(-7 * time.Minute).Format(time.RFC3339Nano)}), PQDegraded, PQReasonEvidenceExpired},
		{"disconnected", map[string]any{"fqdn": "m2.x", "publicKey": "k", "status": "Idle", "quantumReason": "peer-unreachable"}, PQDegraded, PQReasonPeerDisconnected},
		{"phone", connected(map[string]any{"quantumReason": "peer-lacks-profile"}), PQUnsupported, PQReasonPeerLacksProfile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reasonStatus(t, now, tc.peer)
			p := s.Peers[0]
			if p.PQ != tc.pq || p.PQReason != tc.reason {
				t.Fatalf("pq %q reason %q, want %q %q", p.PQ, p.PQReason, tc.pq, tc.reason)
			}
			if p.FileSharing.Available || p.ScreenSharing.Available {
				t.Fatal("unprotected link offered sharing")
			}
			if s.StrictPQReady() {
				t.Fatal("strict readiness with an unprotected peer")
			}
			if tc.pq == PQUnsupported && p.Lifecycle != LifecycleConnected {
				t.Fatalf("not-covered device reported as %q", p.Lifecycle)
			}
			if tc.pq == PQDegraded && p.Lifecycle == LifecycleConnected {
				t.Fatal("degraded link kept the connected lifecycle")
			}
		})
	}
	// A protected link carries no reason even if the runtime sent a stale one.
	s := reasonStatus(t, now, connected(map[string]any{"quantumProfile": experimentalQuantumProfile,
		"quantumKeyInstalledAt": now.Add(-time.Minute).Format(time.RFC3339Nano),
		"quantumKeyExpiresAt":   now.Add(2 * time.Minute).Format(time.RFC3339Nano), "quantumReason": "session-pending"}))
	if s.Peers[0].PQ != PQProtected || s.Peers[0].PQReason != "" {
		t.Fatalf("%+v", s.Peers[0])
	}
	// A phone next to a protected gateway link does not demote the host claim.
	gw := connected(map[string]any{"fqdn": "gw-eu-1.x", "publicKey": "g", "quantumProfile": experimentalQuantumProfile,
		"quantumKeyInstalledAt": now.Add(-time.Minute).Format(time.RFC3339Nano),
		"quantumKeyExpiresAt":   now.Add(2 * time.Minute).Format(time.RFC3339Nano)})
	phone := connected(map[string]any{"fqdn": "iphone.x", "publicKey": "p", "quantumReason": "peer-lacks-profile"})
	input := map[string]any{"peers": map[string]any{"details": []any{gw, phone}}, "management": map[string]any{"connected": true}, "quantumResistance": true}
	data, _ := json.Marshal(input)
	s = SanitizeSnapshot(translateRuntime(data, now))
	if s.PQ != PQProtected || !s.GatewayPQReadyAt(now, 2*time.Minute) {
		t.Fatalf("host pq %q", s.PQ)
	}
	unreachable := connected(map[string]any{"fqdn": "mini.x", "publicKey": "m", "quantumReason": "peer-unreachable"})
	input["peers"] = map[string]any{"details": []any{gw, unreachable}}
	data, _ = json.Marshal(input)
	s = SanitizeSnapshot(translateRuntime(data, now))
	if s.PQ != PQDegraded || !s.GatewayPQReadyAt(now, 2*time.Minute) {
		t.Fatalf("host pq %q with an unreachable Mac", s.PQ)
	}
}
