package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemoteTestResult is what `nexal inference remote-test` reports about a model served by another Mac.
type RemoteTestResult struct {
	Endpoint     string  `json:"endpoint"`
	Model        string  `json:"model"`
	ReachableMs  float64 `json:"reachableMs"`  // GET /v1/models round trip (network + server)
	FirstTokenMs float64 `json:"firstTokenMs"` // request sent -> first generated text
	TotalMs      float64 `json:"totalMs"`
	Tokens       int     `json:"tokens"`
	TokensPerSec float64 `json:"tokensPerSec"` // generation speed after the first token
	Reply        string  `json:"reply"`
	CapabilityOK bool    `json:"capabilityOk"` // the model answered the arithmetic probe correctly
}

// ValidateRemoteEndpoint accepts only an http URL on a neXal mesh address (100.64.0.0/10) or loopback,
// so the test can never be pointed at an arbitrary host.
func ValidateRemoteEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("endpoint must look like http://<neXal address>:8001/v1")
	}
	ip := net.ParseIP(u.Hostname())
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	if ip == nil || !(ip.IsLoopback() || cgnat.Contains(ip)) {
		return nil, errors.New("endpoint must be a neXal mesh address or 127.0.0.1")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	return u, nil
}

// RemoteTest measures reachability, time to first token, generation speed and a one-line capability
// probe against an OpenAI-compatible server. client may be nil.
func RemoteTest(ctx context.Context, endpoint string, client *http.Client) (*RemoteTestResult, error) {
	return remoteRun(ctx, endpoint, client, "What is 17 + 25? Answer with just the number.", 256)
}

func remoteRun(ctx context.Context, endpoint string, client *http.Client, prompt string, maxTokens int) (*RemoteTestResult, error) {
	u, err := ValidateRemoteEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	shown := u.String()
	// A Mac cannot connect to its own neXal address, so when the Mac serving the model is this one, the
	// pool server answers on loopback too: test there.
	if ownAddress(u.Hostname()) {
		u.Host = net.JoinHostPort("127.0.0.1", u.Port())
	}
	base := u.String()
	res := &RemoteTestResult{Endpoint: shown}

	t0 := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", base, err)
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&models)
	resp.Body.Close()
	res.ReachableMs = ms(time.Since(t0))
	res.Model = "default_model"
	if len(models.Data) > 0 && models.Data[0].ID != "" {
		res.Model = models.Data[0].ID
	}

	body, _ := json.Marshal(map[string]any{
		"model": res.Model, "stream": true, "max_tokens": maxTokens, "temperature": 0,
		// Thinking models (Qwen3) would spend the budget reasoning; ask for a direct answer.
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"messages":             []map[string]string{{"role": "user", "content": prompt}},
	})
	t1 := time.Now()
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chat request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chat request returned HTTP %d", resp.StatusCode)
	}
	var first, last time.Time
	var reply strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ch) != nil || len(ch.Choices) == 0 {
			continue
		}
		d := ch.Choices[0].Delta
		if d.Content == "" && d.Reasoning == "" && d.ReasoningContent == "" {
			continue
		}
		now := time.Now()
		if first.IsZero() {
			first = now
		}
		last = now
		res.Tokens++
		reply.WriteString(d.Content) // reasoning counts as speed but is not the answer
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the reply: %w", err)
	}
	if first.IsZero() {
		return nil, errors.New("the model returned no text")
	}
	res.FirstTokenMs = ms(first.Sub(t1))
	res.TotalMs = ms(time.Since(t1))
	if res.Tokens > 1 && last.After(first) {
		res.TokensPerSec = float64(res.Tokens-1) / last.Sub(first).Seconds()
	}
	res.Reply = strings.TrimSpace(reply.String())
	res.CapabilityOK = strings.Contains(res.Reply, "42")
	return res, nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// ownAddress reports whether host is an address of one of this Mac's network interfaces.
func ownAddress(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
