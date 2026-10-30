package remoteinference

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Backend says where a request was answered: on this Mac's own model, or offloaded to a serverless GPU or
// cloud tier (overflow.go).
type Backend int

const (
	Local Backend = iota
	Offload
)

const (
	meterBucket  = 5 * time.Second // one point of the graph
	meterKeep    = 15 * time.Minute
	meterWindow  = 10 * time.Minute // what a snapshot shows
	maxTallyBody = 256 << 10        // a non-streamed answer is read for its token count only up to this size
)

type bucket struct {
	tokens   [2]int64
	requests [2]int
}

// Meter measures what the model server returns: tokens (about one per streamed chunk, or the answer's own usage
// count) per second and finished requests, kept apart for this Mac's model and for offloaded requests, in
// five-second steps. It stores no prompt or answer text. All methods are safe for concurrent use.
type Meter struct {
	mu      sync.Mutex
	now     func() time.Time
	since   time.Time
	buckets map[int64]*bucket // keyed by bucket start, unix seconds
	total   [2]struct{ tokens, requests int64 }
	offload string // the name of the tier that last answered an offloaded request
}

// NewMeter returns an empty meter.
func NewMeter() *Meter {
	return &Meter{now: time.Now, since: time.Now(), buckets: map[int64]*bucket{}}
}

func (m *Meter) at() *bucket {
	k := m.now().Unix() / int64(meterBucket/time.Second) * int64(meterBucket/time.Second)
	b := m.buckets[k]
	if b == nil {
		b = &bucket{}
		m.buckets[k] = b
		for old := range m.buckets { // keep a bounded history
			if k-old > int64(meterKeep/time.Second) {
				delete(m.buckets, old)
			}
		}
	}
	return b
}

// Add counts tokens as they are returned, so the rate follows the answer being written.
func (m *Meter) Add(b Backend, tokens int) {
	if m == nil || tokens <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.at().tokens[b] += int64(tokens)
	m.total[b].tokens += int64(tokens)
}

// Done counts one finished request, plus any tokens only known at the end (a non-streamed answer). name is the
// offload tier that answered; it is ignored for Local.
func (m *Meter) Done(b Backend, lateTokens int, name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	bk := m.at()
	if lateTokens > 0 {
		bk.tokens[b] += int64(lateTokens)
		m.total[b].tokens += int64(lateTokens)
	}
	bk.requests[b]++
	m.total[b].requests++
	if b == Offload && name != "" {
		m.offload = name
	}
}

// Point is one step of the graph: tokens per second and requests finished in that step.
type Point struct {
	T          int64   `json:"t"` // start of the step, unix seconds
	LocalTPS   float64 `json:"localTps"`
	OffloadTPS float64 `json:"offloadTps"`
	LocalReqs  int     `json:"localRequests"`
	OffReqs    int     `json:"offloadRequests"`
}

// Totals are the counts since the connector started.
type Totals struct {
	LocalRequests   int64 `json:"localRequests"`
	LocalTokens     int64 `json:"localTokens"`
	OffloadRequests int64 `json:"offloadRequests"`
	OffloadTokens   int64 `json:"offloadTokens"`
}

// Snapshot is what the app draws.
type Snapshot struct {
	Updated       int64   `json:"updated"` // unix seconds
	Since         int64   `json:"since"`
	BucketSeconds int     `json:"bucketSeconds"`
	OffloadName   string  `json:"offloadName"` // "" until something has been offloaded
	Points        []Point `json:"points"`      // oldest first, one per step including quiet ones
	Totals        Totals  `json:"totals"`
}

// Snapshot returns the last ten minutes, quiet steps included so the line is continuous.
func (m *Meter) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	step := int64(meterBucket / time.Second)
	nowT := m.now().Unix() / step * step
	n := int(meterWindow / meterBucket)
	s := Snapshot{Updated: m.now().Unix(), Since: m.since.Unix(), BucketSeconds: int(step), OffloadName: m.offload,
		Points: make([]Point, 0, n),
		Totals: Totals{LocalRequests: m.total[Local].requests, LocalTokens: m.total[Local].tokens,
			OffloadRequests: m.total[Offload].requests, OffloadTokens: m.total[Offload].tokens}}
	for i := n - 1; i >= 0; i-- {
		t := nowT - int64(i)*step
		p := Point{T: t}
		if b := m.buckets[t]; b != nil {
			p.LocalTPS = float64(b.tokens[Local]) / float64(step)
			p.OffloadTPS = float64(b.tokens[Offload]) / float64(step)
			p.LocalReqs, p.OffReqs = b.requests[Local], b.requests[Offload]
		}
		s.Points = append(s.Points, p)
	}
	return s
}

// busy reports whether anything was counted in the snapshot's window.
func (s Snapshot) busy() bool {
	for _, p := range s.Points {
		if p.LocalTPS > 0 || p.OffloadTPS > 0 || p.LocalReqs > 0 || p.OffReqs > 0 {
			return true
		}
	}
	return false
}

// Publish writes the snapshot to path (mode 0600, replaced atomically) every interval until ctx ends, so the app
// can draw it without asking the running connector. While nothing has happened for the whole window it stops
// rewriting an unchanged file.
func (m *Meter) Publish(done <-chan struct{}, path string, every time.Duration) {
	if m == nil {
		return
	}
	wroteIdle := false
	write := func() {
		s := m.Snapshot()
		if !s.busy() && wroteIdle {
			return
		}
		wroteIdle = !s.busy()
		data, err := json.Marshal(s)
		if err != nil {
			return
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".inference-throughput-*")
		if err != nil {
			return
		}
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Chmod(tmp.Name(), 0o600) != nil || os.Rename(tmp.Name(), path) != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	write()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			write()
		}
	}
}

// tally counts the tokens in one response as its bytes go by. A streamed answer (server-sent events) counts one
// token per data event, which is what the servers send; any other answer is read, up to maxTallyBody, for the
// "usage.completion_tokens" it carries, else about four bytes to a token.
type tally struct {
	pos    int
	line   [7]byte
	isData bool
	body   bytes.Buffer
	size   int64
}

func isStream(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

// feed takes the next bytes and returns the tokens now known (streamed answers only).
func (t *tally) feed(p []byte, h http.Header) int {
	t.size += int64(len(p))
	if !isStream(h) {
		if t.body.Len() < maxTallyBody {
			room := maxTallyBody - t.body.Len()
			if len(p) > room {
				p = p[:room]
			}
			t.body.Write(p)
		}
		return 0
	}
	n := 0
	for _, c := range p {
		if c == '\n' {
			t.pos, t.isData = 0, false
			continue
		}
		if t.pos < len(t.line) {
			t.line[t.pos] = c
		}
		t.pos++
		switch {
		case t.pos == 6 && string(t.line[:6]) == "data: ":
			t.isData = true
		case t.pos == 7 && t.isData && c != '[': // "data: [DONE]" is not a token
			n++
		}
	}
	return n
}

// finish returns the tokens only known at the end of a non-streamed answer.
func (t *tally) finish(h http.Header) int {
	if isStream(h) || t.size == 0 {
		return 0
	}
	var r struct {
		Usage struct {
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(t.body.Bytes(), &r) == nil && r.Usage.Completion > 0 {
		return r.Usage.Completion
	}
	if t.size >= maxTallyBody { // truncated read: estimate from the real size
		return int(t.size / 4)
	}
	return len(t.body.Bytes()) / 4
}
