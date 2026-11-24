package cybersecurity

import (
	"net/netip"
	"testing"
	"time"
)

func TestHoneypotLimiterWindowResetCooloffExpiryAndPruning(t *testing.T) {
	var l honeypotLimiter
	base := time.Now()
	src := netip.MustParseAddr("192.168.1.5")
	hit := func(at time.Time, a netip.Addr, port int) honeypotHit {
		return honeypotHit{at: at, service: "ssh", port: port, source: a}
	}

	if !l.allow(hit(base, src, 22)) {
		t.Fatal("first hit must be allowed")
	}
	if l.allow(hit(base.Add(honeypotSourceCooloff-time.Second), src, 22)) {
		t.Fatal("same source+port inside cool-off must be suppressed")
	}
	if !l.allow(hit(base.Add(honeypotSourceCooloff-time.Second), src, 23)) {
		t.Fatal("different port from the same source is a different key")
	}
	if !l.allow(hit(base.Add(honeypotSourceCooloff), src, 22)) {
		t.Fatal("hit exactly at the cool-off boundary must be allowed again")
	}

	// Exhaust the hourly budget, then confirm the next hour starts a fresh window.
	var l2 honeypotLimiter
	for i := 0; i < honeypotHourlyEvents; i++ {
		if !l2.allow(hit(base, netip.AddrFrom4([4]byte{10, 0, 0, byte(i)}), 22)) {
			t.Fatalf("hit %d should fit in the hourly budget", i)
		}
	}
	if l2.allow(hit(base, netip.MustParseAddr("10.0.1.1"), 22)) {
		t.Fatal("hourly budget exceeded must suppress")
	}
	if !l2.allow(hit(base.Add(time.Hour), netip.MustParseAddr("10.0.1.1"), 22)) || l2.count != 1 {
		t.Fatalf("window should reset after an hour, count=%d", l2.count)
	}

	// Stale keys are pruned once the map grows past 1024 entries.
	var l3 honeypotLimiter
	l3.last = map[string]time.Time{}
	for i := 0; i < 1100; i++ {
		l3.last[string(rune('a'+i%26))+time.Duration(i).String()] = base
	}
	later := base.Add(2 * time.Hour)
	if !l3.allow(hit(later, src, 22)) {
		t.Fatal("hit after window must be allowed")
	}
	if len(l3.last) != 1 {
		t.Fatalf("stale keys should be pruned, have %d", len(l3.last))
	}
}
