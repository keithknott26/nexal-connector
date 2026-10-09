package diaglog

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"
)

// DedupMaxHold bounds how long one suppressed run can stay silent: a message
// repeating for longer is summarised and shown again.
const DedupMaxHold = 5 * time.Minute

var timeField = regexp.MustCompile(`"time":"[^"]*",?`)

// Dedup wraps a line-oriented writer and collapses identical consecutive lines
// (ignoring the JSON "time" field) into the first line plus one
// "(repeated N times in Ts)" note, written when the run ends, DedupMaxHold
// passes, or Flush is called. Each Write must hold whole lines, as slog's
// handlers produce.
type Dedup struct {
	mu      sync.Mutex
	w       io.Writer
	now     func() time.Time
	key     []byte
	start   time.Time
	last    time.Time
	repeats int
}

// NewDedup returns a Dedup writing to w.
func NewDedup(w io.Writer) *Dedup { return &Dedup{w: w, now: time.Now} }

func (d *Dedup) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	k := timeField.ReplaceAll(bytes.TrimSpace(p), nil)
	if d.key != nil && bytes.Equal(k, d.key) && now.Sub(d.start) < DedupMaxHold {
		d.repeats++
		d.last = now
		return len(p), nil
	}
	d.flushLocked()
	d.key, d.start, d.last = k, now, now
	return d.w.Write(p)
}

// Flush writes any pending repeat note.
func (d *Dedup) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flushLocked()
}

func (d *Dedup) flushLocked() {
	if d.repeats > 0 {
		_, _ = fmt.Fprintf(d.w, "(repeated %d times in %s)\n", d.repeats, d.last.Sub(d.start).Round(time.Second))
	}
	d.repeats = 0
}
