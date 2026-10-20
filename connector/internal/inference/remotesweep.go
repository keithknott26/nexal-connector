package inference

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// SweepSample is one call of a speed sweep.
type SweepSample struct {
	Call         int     `json:"call"` // 1-based
	FirstTokenMs float64 `json:"firstTokenMs"`
	TokensPerSec float64 `json:"tokensPerSec"`
	Tokens       int     `json:"tokens"`
	TotalMs      float64 `json:"totalMs"`
	OK           bool    `json:"ok"`
	Error        string  `json:"error,omitempty"`
}

// SweepResult is what `nexal inference remote-test --calls N` reports: the same generation measured many
// times in a row, so a drift or a dropout shows up as a line instead of a single number.
type SweepResult struct {
	Endpoint string        `json:"endpoint"`
	Model    string        `json:"model"`
	Samples  []SweepSample `json:"samples"`
}

const (
	MinSweepCalls = 2
	MaxSweepCalls = 60
	sweepPrompt   = "Write a story of about 150 words about a lighthouse keeper."
	sweepTokens   = 128
)

// RemoteSweep makes calls generations one after another. A failed call is recorded and the sweep goes on;
// it only fails when every call did, or the context ends first.
func RemoteSweep(ctx context.Context, endpoint string, calls int, client *http.Client, progress func(SweepSample)) (*SweepResult, error) {
	if calls < MinSweepCalls || calls > MaxSweepCalls {
		return nil, errors.New("calls must be between 2 and 60")
	}
	if _, err := ValidateRemoteEndpoint(endpoint); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	out := &SweepResult{Endpoint: endpoint}
	good := 0
	var lastErr error
	for i := 1; i <= calls; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s := SweepSample{Call: i}
		res, err := remoteRun(ctx, endpoint, client, sweepPrompt, sweepTokens)
		if err != nil {
			s.Error, lastErr = err.Error(), err
		} else {
			s.OK, s.FirstTokenMs, s.TokensPerSec, s.Tokens, s.TotalMs = true, res.FirstTokenMs, res.TokensPerSec, res.Tokens, res.TotalMs
			out.Endpoint, out.Model = res.Endpoint, res.Model
			good++
		}
		out.Samples = append(out.Samples, s)
		if progress != nil {
			progress(s)
		}
	}
	if good == 0 {
		return nil, lastErr
	}
	return out, nil
}
