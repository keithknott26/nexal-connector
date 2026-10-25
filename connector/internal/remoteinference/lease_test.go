package remoteinference

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// The runner holds the storage-host lease while this Mac answers a request (single serve or pool rank 0),
// renews it, and closes it once nothing is served; a pool member that does not answer never takes it.
func TestHoldLease(t *testing.T) {
	var calls []bool
	r := &Runner{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Lease: func(_ context.Context, open bool) error { calls = append(calls, open); return nil }}
	ctx := context.Background()
	r.holdLease(ctx, []Task{{State: "ready", PoolID: "p", PoolRank: 1}})
	if len(calls) != 0 {
		t.Fatalf("rank 1 took the lease: %v", calls)
	}
	r.holdLease(ctx, []Task{{State: "running", PoolID: "p", PoolRank: 0}})
	r.holdLease(ctx, []Task{{State: "ready", PoolID: "p", PoolRank: 0}}) // too soon to renew
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("open once: %v", calls)
	}
	r.leaseAt = time.Now().Add(-leaseRenew)
	r.holdLease(ctx, []Task{{State: "ready"}})
	r.holdLease(ctx, nil)
	r.holdLease(ctx, nil)
	if len(calls) != 3 || !calls[1] || calls[2] {
		t.Fatalf("renew then close once: %v", calls)
	}
}
