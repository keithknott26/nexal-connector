package pool

import "errors"

var (
	ErrInvalid      = errors.New("pool: invalid input")
	ErrQuota        = errors.New("pool: quota or capacity exceeded")
	ErrHeadroom     = errors.New("pool: insufficient free disk headroom")
	ErrIntegrity    = errors.New("pool: integrity check failed")
	ErrUnsafePath   = errors.New("pool: unsafe path or filesystem entry")
	ErrConflict     = errors.New("pool: version or ownership conflict")
	ErrDurability   = errors.New("pool: two distinct confirmed durable replicas required")
	ErrUnauthorized = errors.New("pool: unauthorized or revoked device")
	ErrExpired      = errors.New("pool: lease or invitation expired")
	ErrPaused       = errors.New("pool: owner has reclaimed capacity")
	ErrClosed       = errors.New("pool: store closed")
	ErrUnsupported  = errors.New("pool: unsupported placement")
	// ErrTimeout is a bounded wait that expired. It exists so a caller can tell
	// "nothing arrived in time" apart from "the input was wrong": in the ring
	// collective the first is a failed rank and the second is a bug.
	ErrTimeout = errors.New("pool: bounded wait expired")
	// ErrRankUnreachable is a collective rank this host could not reach or did not
	// hear from inside its step budget. It is always wrapped in a RingFailure that
	// names the rank, so survivors report a rank rather than hanging on it.
	ErrRankUnreachable = errors.New("pool: ring rank unreachable")
	// ErrAborted is another rank's report that the collective cannot complete.
	ErrAborted = errors.New("pool: collective aborted by a peer")
)
