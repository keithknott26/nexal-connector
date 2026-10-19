package bandwidth

import (
	"log/slog"
	"testing"
	"time"

	"nexal/connector/internal/mesh"
)

type staticProvider struct{ status mesh.Status }

func (p staticProvider) Snapshot() mesh.Status { return p.status }

func TestPeerBandwidthExpiry(t *testing.T) {
	r := NewRunner(slog.Default())

	// Store a result.
	r.mu.Lock()
	r.results["peer-1"] = Result{
		PeerID:       "peer-1",
		DownloadMbps: 42.5,
		MeasuredAt:   time.Now(),
	}
	r.mu.Unlock()

	// Should be returned while fresh.
	if got := r.PeerBandwidth("peer-1"); got != 42.5 {
		t.Errorf("PeerBandwidth = %f, want 42.5", got)
	}

	// Unknown peer returns 0.
	if got := r.PeerBandwidth("unknown"); got != 0 {
		t.Errorf("PeerBandwidth(unknown) = %f, want 0", got)
	}

	// Expire the result.
	r.mu.Lock()
	old := r.results["peer-1"]
	old.MeasuredAt = time.Now().Add(-3 * TestInterval)
	r.results["peer-1"] = old
	r.mu.Unlock()

	if got := r.PeerBandwidth("peer-1"); got != 0 {
		t.Errorf("expired PeerBandwidth = %f, want 0", got)
	}
}

func TestObservedMax(t *testing.T) {
	r := NewRunner(slog.Default())

	// Default when no tests run.
	if got := r.ObservedMax(); got != 50 {
		t.Errorf("default ObservedMax = %f, want 50", got)
	}

	// After recording a result.
	r.mu.Lock()
	r.observedMax = 85
	r.mu.Unlock()

	got := r.ObservedMax()
	// 85 * 1.1 = 93.5 → should round to 100.
	if got != 100 {
		t.Errorf("ObservedMax = %f, want 100", got)
	}
}

func TestCeilToNice(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{3, 5},
		{5, 5},
		{10, 10},
		{11, 15},
		{20, 20},
		{23, 25},
		{48, 50},
		{74, 75},
		{90, 100},
		{140, 150},
		{180, 200},
		{240, 250},
		{280, 300},
		{450, 500},
		{700, 750},
		{950, 1000},
		{1200, 1500},
		{2300, 2500},
	}
	for _, tt := range tests {
		if got := ceilToNice(tt.in); got != tt.want {
			t.Errorf("ceilToNice(%g) = %g, want %g", tt.in, got, tt.want)
		}
	}
}

func TestResultsFilterExpired(t *testing.T) {
	r := NewRunner(slog.Default())
	now := time.Now()

	r.mu.Lock()
	r.results["fresh"] = Result{PeerID: "fresh", DownloadMbps: 50, MeasuredAt: now}
	r.results["stale"] = Result{PeerID: "stale", DownloadMbps: 30, MeasuredAt: now.Add(-3 * TestInterval)}
	r.mu.Unlock()

	results := r.Results()
	if _, ok := results["fresh"]; !ok {
		t.Error("fresh result should be returned")
	}
	if _, ok := results["stale"]; ok {
		t.Error("stale result should be filtered out")
	}
}

func TestNextDelayRetriesSoonThenBacksOff(t *testing.T) {
	if got := nextDelay(false, 0); got != TestInterval {
		t.Fatalf("all peers measured: %v, want %v", got, TestInterval)
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, TestInterval, TestInterval}
	for i, w := range want {
		if got := nextDelay(true, i); got != w {
			t.Errorf("retry %d: %v, want %v", i, got, w)
		}
	}
}

func TestRunsServerSkipsGatewaysAndPhones(t *testing.T) {
	cases := map[string]bool{
		"studio":       true,
		"gw-sfo":       false,
		"GW-nyc":       false,
		"Keith-iPhone": false,
		"family-iPad":  false,
	}
	for name, want := range cases {
		if got := runsServer(mesh.Peer{Name: name}); got != want {
			t.Errorf("runsServer(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHasUnmeasuredPeer(t *testing.T) {
	r := NewRunner(slog.Default())
	peer := func(id, name string, lc mesh.Lifecycle, addr string) mesh.Peer {
		return mesh.Peer{ID: id, Name: name, Lifecycle: lc, TunnelAddress: addr}
	}
	has := func(peers ...mesh.Peer) bool {
		return r.hasUnmeasuredPeer(staticProvider{mesh.Status{Peers: peers}})
	}

	if has() {
		t.Error("no peers: want false")
	}
	// Ineligible peers never count as unmeasured.
	if has(
		peer("a", "studio", mesh.LifecycleOffline, "100.64.0.2"),
		peer("b", "studio2", mesh.LifecycleConnected, ""),
		peer("c", "gw-sfo", mesh.LifecycleConnected, "100.64.0.3"),
		peer("d", "my-iPhone", mesh.LifecycleConnected, "100.64.0.4"),
	) {
		t.Error("ineligible peers: want false")
	}
	if !has(peer("e", "studio", mesh.LifecycleConnected, "100.64.0.5")) {
		t.Error("connected peer without reading: want true")
	}

	r.mu.Lock()
	r.results["e"] = Result{PeerID: "e", DownloadMbps: 10, MeasuredAt: time.Now()}
	r.mu.Unlock()
	if has(peer("e", "studio", mesh.LifecycleConnected, "100.64.0.5")) {
		t.Error("fresh reading: want false")
	}
}
