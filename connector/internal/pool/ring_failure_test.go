package pool

import (
	"errors"
	"testing"
)

// RingFailure is how a survivor names the dead rank, so its message must carry
// the rank and fingerprint and it must unwrap to the transport error beneath.
func TestRingFailureErrorAndUnwrap(t *testing.T) {
	cause := errors.New("connection reset")
	f := &RingFailure{Rank: 2, Fingerprint: "ab:cd", Reason: "successor unreachable", err: cause}

	if got, want := f.Error(), "pool: ring rank 2 (ab:cd): successor unreachable"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(f, cause) {
		t.Fatalf("RingFailure does not unwrap to its cause")
	}
	if (&RingFailure{}).Unwrap() != nil {
		t.Fatalf("Unwrap() of a failure with no cause must be nil")
	}
}
