package remoteinference

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Tier is one OpenAI-compatible chat endpoint a request can be sent to besides the local Mac: a serverless GPU
// (RunPod: https://api.runpod.ai/v2/<endpoint id>/openai/v1) or a cloud API (OpenAI, Gemini's and Anthropic's
// OpenAI-compatible bases). All speak the same protocol, so one client serves them all.
type Tier struct {
	Name      string
	BaseURL   string        // up to and including /v1; "/chat/completions" is appended
	APIKey    string        // sent as a Bearer token; read from the Keychain by the caller, never logged
	Model     string        // the model id this tier answers to; the request's "model" is rewritten to it
	FirstByte time.Duration // how long to wait for response headers before trying the next tier (0: 90s)
}

// Overflow routes a request to the local pool, a bigger serverless-GPU model, or a cloud API. It is off until
// Enabled, and a peer is served by a paid tier only if listed (CONTRACT.md: no automatic paid fallback).
type Overflow struct {
	mu         sync.Mutex
	Enabled    bool
	GPUPeers   map[string]bool // peer IPs allowed to use the GPU tier; "*" is everyone
	CloudPeers map[string]bool // peer IPs allowed to use the cloud tier (a separate consent)
	Always     bool            // every chat request goes to the GPU tier (for peers allowed it)
	HardAlias  string          // a request naming this model asks for the GPU tier outright
	MaxWait    time.Duration   // go to the GPU when the local queue would take longer than this
	MaxPrompt  int             // go to the GPU when the prompt is over this many (estimated) tokens; 0 = never
	DailyLimit int             // paid-tier requests per day; 0 = unlimited
	GPU, Cloud *Tier
	Meter      *Meter // nil: offloaded answers are not measured

	avg  time.Duration // moving average of a local request's duration
	day  string
	used int
}

// SetMeter makes offloaded answers count in m.
func (o *Overflow) SetMeter(m *Meter) {
	o.mu.Lock()
	o.Meter = m
	maxPrompt := o.MaxPrompt
	o.mu.Unlock()
	m.SetThreshold(maxPrompt)
}

// Apply switches offloading to a policy another Mac set, live: the next request follows it. Peers allowed to use the
// GPU stay as configured here, or everyone when none were. It returns what to tell that Mac.
func (o *Overflow) Apply(p OffloadPolicy) string {
	o.mu.Lock()
	o.Enabled = p.Mode == "always" || p.Mode == "large"
	o.Always = p.Mode == "always"
	o.MaxPrompt, o.MaxWait = 0, 0
	if p.Mode == "large" {
		o.MaxPrompt, o.MaxWait = p.MaxPromptTokens, time.Duration(p.MaxWaitSeconds)*time.Second
	}
	if o.Enabled && len(o.GPUPeers) == 0 && len(o.CloudPeers) == 0 {
		o.GPUPeers = map[string]bool{"*": true}
	}
	meter, maxPrompt, name := o.Meter, o.MaxPrompt, "the GPU"
	if o.GPU != nil && o.GPU.Name != "" {
		name = o.GPU.Name
	} else if o.GPU == nil && o.Cloud != nil && o.Cloud.Name != "" {
		name = o.Cloud.Name
	}
	o.mu.Unlock()
	meter.SetThreshold(maxPrompt)
	switch {
	case p.Mode == "always":
		return "Applied: every request goes to " + name + "."
	case p.Mode == "large" && p.MaxPromptTokens > 0 && p.MaxWaitSeconds > 0:
		return fmt.Sprintf("Applied: prompts over %d tokens, or a queue over %d s, go to %s.", p.MaxPromptTokens, p.MaxWaitSeconds, name)
	case p.Mode == "large" && p.MaxPromptTokens > 0:
		return fmt.Sprintf("Applied: prompts over %d tokens go to %s.", p.MaxPromptTokens, name)
	case p.Mode == "large":
		return fmt.Sprintf("Applied: requests go to %s when the queue here is over %d s.", name, p.MaxWaitSeconds)
	default:
		return "Applied: offloading is off; this Mac's model answers every request."
	}
}

// Observe records how long a local request took, for the queue-wait estimate.
func (o *Overflow) Observe(d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.avg == 0 {
		o.avg = d
	} else {
		o.avg = (o.avg*7 + d) / 8
	}
}

// Plan is the ordered tiers to try for a request; empty means the local pool.
func (o *Overflow) Plan(peer, model string, promptTokens, inflight int) (tiers []*Tier, hard, limited bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.Enabled {
		return nil, false, false
	}
	hard = o.HardAlias != "" && model == o.HardAlias
	wait := time.Duration(inflight) * o.avg
	slow := o.MaxWait > 0 && wait > o.MaxWait
	big := o.MaxPrompt > 0 && promptTokens > o.MaxPrompt
	if !(hard || slow || big || o.Always) {
		return nil, false, false
	}
	if o.GPU != nil && (o.GPUPeers["*"] || o.GPUPeers[peer]) {
		tiers = append(tiers, o.GPU)
	}
	if o.Cloud != nil && (o.CloudPeers["*"] || o.CloudPeers[peer]) {
		tiers = append(tiers, o.Cloud)
	}
	if len(tiers) > 0 && o.DailyLimit > 0 {
		today := time.Now().UTC().Format("2006-01-02")
		if o.day != today {
			o.day, o.used = today, 0
		}
		if o.used >= o.DailyLimit {
			return nil, hard, true
		}
		o.used++
	}
	return tiers, hard, false
}

// Serve tries the overflow tiers for a chat request; it returns true when it answered. A false return means
// the request is untouched (body restored) and should go to the local pool.
func (o *Overflow) Serve(w http.ResponseWriter, r *http.Request, inflight int) bool {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") || r.Body == nil {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20+1))
	_ = r.Body.Close()
	restore := func() {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	if err != nil || len(body) > 64<<20 {
		restore()
		return false
	}
	var m map[string]json.RawMessage
	var model string
	if json.Unmarshal(body, &m) != nil || json.Unmarshal(m["model"], &model) != nil {
		restore()
		return false
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	tiers, hard, limited := o.Plan(peer, model, len(body)/4, inflight)
	o.mu.Lock()
	meter, maxPrompt := o.Meter, o.MaxPrompt
	o.mu.Unlock()
	meter.SetThreshold(maxPrompt)
	if len(tiers) == 0 {
		restore()
		if !limited && !hard {
			meter.Prompt(Local, len(body)/4)
		}
		if limited {
			fail(w, http.StatusTooManyRequests, "The daily limit for the large model is used up.")
			return true
		}
		if hard { // asked for the GPU model, which this peer may not use
			fail(w, http.StatusForbidden, "The "+o.HardAlias+" model is not enabled for this peer.")
			return true
		}
		return false
	}
	for _, t := range tiers {
		if o.try(w, r, t, m) {
			meter.Prompt(Offload, len(body)/4)
			return true
		}
	}
	if hard {
		fail(w, http.StatusBadGateway, "The large model did not answer in time.")
		return true
	}
	restore()
	meter.Prompt(Local, len(body)/4) // every tier failed: the local pool answers
	return false
}

// try sends the request to one tier; false means nothing reached the caller (the tier timed out or errored
// before its headers), so the next tier or the local pool can answer instead.
func (o *Overflow) try(w http.ResponseWriter, r *http.Request, t *Tier, m map[string]json.RawMessage) bool {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	out["model"], _ = json.Marshal(t.Model)
	payload, err := json.Marshal(out)
	if err != nil {
		return false
	}
	fb := t.FirstByte
	if fb <= 0 {
		fb = 90 * time.Second
	}
	c := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: fb}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+t.APIKey)
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return false
	}
	for k, v := range resp.Header {
		if strings.EqualFold(k, "content-length") {
			continue
		}
		w.Header()[k] = v
	}
	w.Header().Set("x-nexal-backend", t.Name) // a caller can always see where its prompt ran
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 4096)
	o.mu.Lock()
	meter := o.Meter
	o.mu.Unlock()
	var tl tally
	count := resp.StatusCode < 400 // an error body is not an answer
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if count {
				meter.Add(Offload, tl.feed(buf[:n], resp.Header))
			}
			_, _ = w.Write(buf[:n])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	if count && tl.size > 0 {
		meter.Done(Offload, tl.finish(resp.Header), t.Name)
	}
	return true
}
