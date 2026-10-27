//go:build !darwin

package wol

import (
	"context"
	"testing"
)

func TestNonDarwinWakeStubs(t *testing.T) {
	ctx := context.Background()
	if got := wakeForNetwork(ctx); got != WakeUnknown {
		t.Fatalf("wakeForNetwork = %q, want %q", got, WakeUnknown)
	}
	if got := hardwarePortMACs(ctx); len(got) != 0 {
		t.Fatalf("hardwarePortMACs = %v, want none", got)
	}
}
