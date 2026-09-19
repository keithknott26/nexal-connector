package agent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand"
	"time"

	"nexal/connector/internal/client"
)

const Template = "monte-carlo-pi-v1"
const MaxSamples int64 = 100_000_000

// A conservative admission floor includes Go process/runtime headroom; the
// workload itself streams samples using constant memory and one CPU goroutine.
const WorkloadMemoryBytes uint64 = 64 << 20

func ValidateAttempt(a client.Attempt, host string, now time.Time) error {
	if !client.ValidID(a.ID) || !client.ValidID(a.JobID) || a.HostID != host {
		return errors.New("invalid or wrong-host attempt")
	}
	if a.Template != Template {
		return errors.New("unknown workload template")
	}
	if a.Samples < 1 || a.Samples > MaxSamples || a.MaxCostCents < 0 {
		return errors.New("invalid workload limits")
	}
	if !a.LeaseExpiresAt.After(now) || a.LeaseExpiresAt.After(now.Add(2*time.Minute)) {
		return errors.New("stale or overlong execution lease")
	}
	return nil
}

// MonteCarlo is the only executable workload. It accepts no source, executable,
// input URL, filesystem path, environment, model or user-provided code.
func MonteCarlo(ctx context.Context, a client.Attempt, deadline func() time.Time) (client.Result, error) {
	if a.Template != Template || a.Samples < 1 || a.Samples > MaxSamples {
		return client.Result{}, errors.New("unsupported workload")
	}
	sum := sha256.Sum256([]byte(a.ID))
	rng := rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(sum[:8]))))
	var inside int64
	for i := int64(0); i < a.Samples; i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return client.Result{}, err
			}
			if !time.Now().Before(deadline()) {
				return client.Result{}, errors.New("execution lease expired")
			}
		}
		x, y := rng.Float64(), rng.Float64()
		if x*x+y*y <= 1 {
			inside++
		}
	}
	if err := ctx.Err(); err != nil {
		return client.Result{}, err
	}
	if !time.Now().Before(deadline()) {
		return client.Result{}, errors.New("execution lease expired")
	}
	return client.Result{Samples: a.Samples, Inside: inside, Pi: 4 * float64(inside) / float64(a.Samples)}, nil
}
