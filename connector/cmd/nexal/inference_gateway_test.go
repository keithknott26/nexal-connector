package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"
)

// A Mac that is not enrolled (no readable config, or no host id) must just
// serve: the lease helper returns a callable no-op stop and no DNS name.
func TestHoldStorageHostLeaseNoopWhenNotEnrolled(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "config.json")
	stop, dns := holdStorageHostLease(context.Background(), missing, io.Discard, true)
	if stop == nil || dns != "" {
		t.Fatalf("missing config: stop=%v dns=%q", stop != nil, dns)
	}
	stop()

	path := bundleTestConfig(t, "")
	stop, dns = storageHostLease(context.Background(), path, io.Discard)
	if stop == nil || dns != "" {
		t.Fatalf("no host id: stop=%v dns=%q", stop != nil, dns)
	}
	stop()
}
