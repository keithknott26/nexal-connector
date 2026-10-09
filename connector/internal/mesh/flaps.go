package mesh

import (
	"sync"
	"time"
)

// flapWindow is how far back path changes are counted.
const flapWindow = time.Hour

// pathFlaps counts direct<->relay transitions per peer over a rolling window.
// It is in-memory and local-only; nothing here reaches the coordinator report.
type pathFlaps struct {
	mu   sync.Mutex
	last map[string]PathKind
	at   map[string][]time.Time
	seen map[string]time.Time
}

var flapTracker = &pathFlaps{last: map[string]PathKind{}, at: map[string][]time.Time{}, seen: map[string]time.Time{}}

// observe records the peer's current path and returns the number of
// direct<->relay changes within the last hour. Other paths (unknown) neither
// count nor reset the remembered direct/relay state.
func (f *pathFlaps) observe(id string, path PathKind, now time.Time) int {
	if id == "" {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen[id] = now
	if path == PathDirect || path == PathRelay {
		if prev, ok := f.last[id]; ok && prev != path {
			f.at[id] = append(f.at[id], now)
		}
		f.last[id] = path
	}
	cut := now.Add(-flapWindow)
	ts := f.at[id]
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	if i > 0 {
		ts = append([]time.Time(nil), ts[i:]...)
		f.at[id] = ts
	}
	if len(f.seen) > 256 { // drop peers not seen for a window
		for k, t := range f.seen {
			if t.Before(cut) {
				delete(f.seen, k)
				delete(f.last, k)
				delete(f.at, k)
			}
		}
	}
	return len(ts)
}
