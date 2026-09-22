package throttle

import (
	"context"
	"errors"
	"io"
)

// A RATE LIMIT IS NOT A VOLUME CAP, which is why this file exists.
//
// Limiter bounds bytes per second. HARDENING-PLAN §16 requires something different: a
// per-donor MONTHLY budget, because "many consumer ISPs cap around 1.2 TB per month" and
// "a donor holding several backups, plus repair traffic, plus relay duty, can cross that
// and get billed by their own ISP. That is how you lose donors and earn bad press."
// MeteredCeilingBytesPerSecond (64 KiB/s) sustained is ~165 GB/month, so the strictest
// rate this package can impose still walks a donor into a cap.
//
// The volume decision does not belong here: it needs a window that survives process
// restarts, which means it needs the coordinator (internal/budget). So this file is only
// the SEAM — one interface plus metered Reader/Writer constructors — and the policy lives
// where the persisted window lives. Keeping the seam in this package is what makes the
// two enforcement dimensions impossible to apply separately: any transfer that is shaped
// is also charged, because it is the same Writer.

// ErrBudgetExhausted is returned by a shaped Reader or Writer when the §16 monthly
// budget has no bytes left. It is a distinct sentinel so a caller can tell "the donor's
// monthly volume is spent, come back when the window rolls" from a transport failure —
// a retry loop that treats it as an I/O error would hammer a cap it cannot pass.
var ErrBudgetExhausted = errors.New("monthly bandwidth budget exhausted")

// Meter accounts bytes against a volume budget.
//
// Charge is called BEFORE the bytes move, for the same reason Limiter reserves before
// writing: charging afterwards means the payload is already on the donor's uplink and
// already on their ISP bill by the time the budget notices. It must be cheap and must
// never block on the network — the implementation in internal/budget accumulates locally
// and reconciles with the coordinator on its own schedule, so a coordinator that is slow
// or unreachable slows nothing here.
//
// A nil Meter is not a permitted way to opt out of the budget: a shaped transfer either
// has a meter (NewMeteredWriter/NewMeteredReader) or it is an unbudgeted path that must
// be reviewed as such. The plain constructors remain for callers that pre-date the
// budget, and their status surfaces must not claim a volume cap they do not have.
type Meter interface {
	// Charge reports n bytes about to move. It returns ErrBudgetExhausted (wrapped or
	// not) when the budget cannot fund them, and nil otherwise. It never returns a
	// partial grant: chunking is the caller's job and Limiter.Burst() already sets the
	// chunk size.
	Charge(n int64) error
}

// NewMeteredWriter shapes AND budgets a stream. Separate constructor rather than a
// mutable field on Writer: a transfer must not be able to acquire or drop its budget
// half way through, and a reviewer can see from the call site which paths are budgeted.
func NewMeteredWriter(ctx context.Context, w io.Writer, l *Limiter, m Meter) (*Writer, error) {
	writer, err := NewWriter(ctx, w, l)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("metered writer requires a meter")
	}
	writer.meter = m
	return writer, nil
}

// NewMeteredReader is the symmetric case: a restore fanning in, or a local source feeding
// an upload. Download volume counts against a residential cap on most plans, and §16's
// table is explicit that restore "fans in from many donors at once", so the read side is
// metered rather than assumed free.
func NewMeteredReader(ctx context.Context, r io.Reader, l *Limiter, m Meter) (*Reader, error) {
	reader, err := NewReader(ctx, r, l)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("metered reader requires a meter")
	}
	reader.meter = m
	return reader, nil
}

// charge is the single place both shaped types consult the budget, so the two can never
// drift on ordering: budget first, then the rate wait, then the byte move. Budget before
// rate because a refusal must not cost a shaping delay first — an exhausted donor should
// learn that immediately rather than after sleeping for a chunk they are not allowed to
// send.
func charge(m Meter, n int64) error {
	if m == nil {
		return nil
	}
	return m.Charge(n)
}
