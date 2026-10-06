package mesh

import (
	"testing"
	"time"
)

func TestPeerLooksOffline(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name             string
		since, handshake time.Time
		want             bool
	}{
		{"just started connecting", now.Add(-20 * time.Second), time.Time{}, false},
		{"connecting a long time, never shook hands", now.Add(-10 * time.Minute), time.Time{}, true},
		{"connecting a long time, handshake an hour ago (phone off)", now.Add(-10 * time.Minute), now.Add(-time.Hour), true},
		{"reconnecting after a recent handshake", now.Add(-2 * time.Minute), now.Add(-time.Minute), false},
		{"unknown since, no handshake", time.Time{}, time.Time{}, true},
	}
	for _, c := range cases {
		if got := PeerLooksOffline(c.since, c.handshake, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTranslateRuntimeMarksALongConnectingPeerOffline(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	out := []byte(`{"management":{"connected":true},"signal":{"connected":true},"peers":{"details":[
		{"fqdn":"iphone.peers.mesh.nexal.systems","netbirdIp":"100.86.28.93","publicKey":"k1","status":"Connecting",
		 "lastStatusUpdate":"2026-10-06T11:40:00Z","lastWireguardHandshake":"2026-10-06T10:00:00Z"}]}}`)
	s := translateRuntime(out, now)
	if len(s.Peers) != 1 || s.Peers[0].Lifecycle != LifecycleOffline {
		t.Fatalf("want offline, got %+v", s.Peers)
	}
}
