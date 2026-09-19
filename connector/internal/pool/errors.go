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
)
