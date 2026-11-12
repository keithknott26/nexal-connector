package mesh

import (
	"testing"
	"time"
)

func TestPathFlapsRollingHour(t *testing.T) {
	f := &pathFlaps{last: map[string]PathKind{}, at: map[string][]time.Time{}, seen: map[string]time.Time{}}
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	if n := f.observe("p", PathDirect, t0); n != 0 {
		t.Fatalf("first observation counted: %d", n)
	}
	f.observe("p", PathRelay, t0.Add(time.Minute))
	f.observe("p", PathUnknown, t0.Add(2*time.Minute)) // no reset, no count
	f.observe("p", PathRelay, t0.Add(3*time.Minute))   // same path
	if n := f.observe("p", PathDirect, t0.Add(4*time.Minute)); n != 2 {
		t.Fatalf("want 2 flaps, got %d", n)
	}
	if n := f.observe("p", PathDirect, t0.Add(66*time.Minute)); n != 0 {
		t.Fatalf("flaps should age out, got %d", n)
	}
	if n := f.observe("other", PathDirect, t0); n != 0 {
		t.Fatalf("peers must be independent, got %d", n)
	}
}

func TestPathFlapsEmptyIDIgnored(t *testing.T) {
	f := &pathFlaps{last: map[string]PathKind{}, at: map[string][]time.Time{}, seen: map[string]time.Time{}}
	if n := f.observe("", PathDirect, time.Now()); n != 0 {
		t.Fatalf("empty id counted: %d", n)
	}
	if len(f.seen) != 0 || len(f.last) != 0 {
		t.Fatalf("empty id must not be tracked: seen=%d last=%d", len(f.seen), len(f.last))
	}
}

func TestPathFlapsEvictsStalePeers(t *testing.T) {
	f := &pathFlaps{last: map[string]PathKind{}, at: map[string][]time.Time{}, seen: map[string]time.Time{}}
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 257; i++ {
		id := "old" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		f.observe(id, PathDirect, t0)
	}
	// A much later observation triggers cleanup of everything older than the window.
	f.observe("fresh", PathRelay, t0.Add(2*flapWindow))
	if len(f.seen) != 1 || len(f.last) != 1 {
		t.Fatalf("stale peers not evicted: seen=%d last=%d", len(f.seen), len(f.last))
	}
	if _, ok := f.seen["fresh"]; !ok {
		t.Fatal("fresh peer must be retained")
	}
}
