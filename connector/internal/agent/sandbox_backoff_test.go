package agent

import (
	"testing"
	"time"
)

func TestNextSandboxBackoffStartsAtMinDoublesAndCaps(t *testing.T) {
	const min, max = 30 * time.Second, 5 * time.Minute
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, max, max}
	var cur time.Duration
	for i, w := range want {
		cur = nextSandboxBackoff(cur, min, max)
		if cur != w {
			t.Fatalf("step %d: got %v, want %v", i, cur, w)
		}
	}
}
