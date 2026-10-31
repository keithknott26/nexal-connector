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

	reqs      []promptSample // recent requests: how big the prompt was and where it was answered
	threshold int            // the "long prompt" limit (estimated tokens) above which requests are offloaded; 0 = none
}

const maxPromptSamples = 400

type promptSample struct {
	t      int64
	tokens int
	off    bool
}

// Prompt records one chat request's (estimated) prompt size and where it was answered, so the app can show the
// offload limit against the requests around it. No prompt text is kept.
func (m *Meter) Prompt(b Backend, tokens int) {
	if m == nil || tokens <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, promptSample{t: m.now().Unix(), tokens: tokens, off: b == Offload})
	if len(m.reqs) > maxPromptSamples {
		m.reqs = m.reqs[len(m.reqs)-maxPromptSamples:]
	}
}

// SetThreshold sets the prompt-size limit the app draws as a line (0: none).
func (m *Meter) SetThreshold(tokens int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.threshold = tokens
	m.mu.Unlock()
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

// Request is one chat request in the graph of prompt sizes.
type Request struct {
	T            int64 `json:"t"` // unix seconds
	PromptTokens int   `json:"promptTokens"`
	Offloaded    bool  `json:"offloaded"`
}

// Snapshot is what the app draws.
type Snapshot struct {
	Updated       int64   `json:"updated"` // unix seconds
	Since         int64   `json:"since"`
	BucketSeconds int     `json:"bucketSeconds"`
	OffloadName   string  `json:"offloadName"` // "" until something has been offloaded
	Points        []Point `json:"points"`      // oldest first, one per step including quiet ones
	Totals        Totals  `json:"totals"`
	// The prompt-size graph: recent requests, and the limit above which they are offloaded (0: no limit set).
	Requests        []Request `json:"requests"`
	MaxPromptTokens int       `json:"maxPromptTokens"`
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
			OffloadRequests: m.total[Offload].requests, OffloadTokens: m.total[Offload].tokens},
		Requests: []Request{}, MaxPromptTokens: m.threshold}
	for _, r := range m.reqs {
		if r.t >= m.now().Unix()-int64(meterWindow/time.Second) {
			s.Requests = append(s.Requests, Request{T: r.t, PromptTokens: r.tokens, Offloaded: r.off})
		}
	}
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
// can draw it without asking the running connector. While nothing has happened for the whole window it rewrites
// the file only every 10 s, enough to show the connector is alive.
func (m *Meter) Publish(done <-chan struct{}, path string, every time.Duration) {
	if m == nil {
		return
	}
	// While idle the file still gets a fresh "updated" time every idleEvery: the app treats a snapshot older than
	// 20 s as "the connector is not running", so a file written once and then left alone would hide the graph
	// whenever the model has been quiet for half a minute.
	const idleEvery = 10 * time.Second
	var lastWrite time.Time
	write := func() {
		s := m.Snapshot()
		if !s.busy() && !lastWrite.IsZero() && time.Since(lastWrite) < idleEvery {
			return
		}
		lastWrite = time.Now()
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

// Stats is the compact summary a Mac serving a model attaches to its coordinator heartbeat (no extra request), so
// another Mac can chart it without talking to this one. Per-minute counts, the latest prompt sizes and the
// offload limit; no prompt or answer text. The coordinator validates every field (src/inference-stats.ts).
type Stats struct {
	At              int64         `json:"at"`
	Since           int64         `json:"since"`
	OffloadName     string        `json:"offloadName"`
	LocalRequests   int64         `json:"localRequests"`
	LocalTokens     int64         `json:"localTokens"`
	OffloadRequests int64         `json:"offloadRequests"`
	OffloadTokens   int64         `json:"offloadTokens"`
	MaxPromptTokens int           `json:"maxPromptTokens"`
	Minutes         []StatsMinute `json:"minutes"`
	Prompts         []StatsPrompt `json:"prompts"`
}

// StatsMinute is one minute: tokens (lt, ot) and finished requests (lr, or) answered here and offloaded.
type StatsMinute struct {
	T  int64 `json:"t"`
	LT int64 `json:"lt"`
	OT int64 `json:"ot"`
	LR int   `json:"lr"`
	OR int   `json:"or"`
}

// StatsPrompt is one chat request: estimated prompt tokens and whether it was offloaded.
type StatsPrompt struct {
	T int64 `json:"t"`
	P int   `json:"p"`
	O bool  `json:"o"`
}

const (
	statsMinutes = 12 // the coordinator accepts at most this many
	statsPrompts = 24
)

// Stats returns the summary, or nil while nothing has been answered (there is nothing to report yet).
func (m *Meter) Stats() *Stats {
	if m == nil {
		return nil
	}
	s := m.Snapshot()
	if s.Totals == (Totals{}) && len(s.Requests) == 0 {
		return nil
	}
	st := &Stats{At: s.Updated, Since: s.Since, OffloadName: s.OffloadName, MaxPromptTokens: s.MaxPromptTokens,
		LocalRequests: s.Totals.LocalRequests, LocalTokens: s.Totals.LocalTokens,
		OffloadRequests: s.Totals.OffloadRequests, OffloadTokens: s.Totals.OffloadTokens,
		Minutes: []StatsMinute{}, Prompts: []StatsPrompt{}}
	if len(st.OffloadName) > 40 {
		st.OffloadName = st.OffloadName[:40]
	}
	step := float64(s.BucketSeconds)
	for _, p := range s.Points {
		minute := p.T / 60 * 60
		if n := len(st.Minutes); n == 0 || st.Minutes[n-1].T != minute {
			st.Minutes = append(st.Minutes, StatsMinute{T: minute})
		}
		cur := &st.Minutes[len(st.Minutes)-1]
		cur.LT += int64(p.LocalTPS*step + 0.5)
		cur.OT += int64(p.OffloadTPS*step + 0.5)
		cur.LR += p.LocalReqs
		cur.OR += p.OffReqs
	}
	if len(st.Minutes) > statsMinutes {
		st.Minutes = st.Minutes[len(st.Minutes)-statsMinutes:]
	}
	reqs := s.Requests
	if len(reqs) > statsPrompts {
		reqs = reqs[len(reqs)-statsPrompts:]
	}
	for _, r := range reqs {
		st.Prompts = append(st.Prompts, StatsPrompt{T: r.T, P: r.PromptTokens, O: r.Offloaded})
	}
	return st
}
