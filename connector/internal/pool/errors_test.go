package pool

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSentinelErrorsAreDistinctAndPrefixed(t *testing.T) {
	all := []error{
		ErrInvalid, ErrQuota, ErrHeadroom, ErrIntegrity, ErrUnsafePath,
		ErrConflict, ErrDurability, ErrUnauthorized, ErrExpired, ErrPaused,
		ErrClosed, ErrUnsupported, ErrTimeout, ErrRankUnreachable, ErrAborted,
	}
	seen := map[string]bool{}
	for i, e := range all {
		if !strings.HasPrefix(e.Error(), "pool: ") {
			t.Errorf("%q lacks the pool: prefix", e)
		}
		if seen[e.Error()] {
			t.Errorf("duplicate message %q", e)
		}
		seen[e.Error()] = true
		for j, o := range all {
			if i != j && errors.Is(e, o) {
				t.Errorf("%q unexpectedly matches %q", e, o)
			}
		}
	}
}

func TestSentinelErrorsSurviveWrapping(t *testing.T) {
	wrapped := fmt.Errorf("rank 2: %w", ErrTimeout)
	if !errors.Is(wrapped, ErrTimeout) || errors.Is(wrapped, ErrInvalid) {
		t.Fatalf("wrapped sentinel misclassified: %v", wrapped)
	}
}
