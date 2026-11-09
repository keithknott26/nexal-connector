package agent

import (
	"context"
	"net/http"
	"testing"
	"time"

	"nexal/connector/internal/client"
)

type rejectingAPI struct {
	fakeAPI
	status int
	calls  int
}

func (r *rejectingAPI) Heartbeat(context.Context, string, client.Heartbeat) (client.HeartbeatResponse, error) {
	r.calls++
	if r.status == 0 {
		return client.HeartbeatResponse{}, nil
	}
	return client.HeartbeatResponse{}, &client.StatusError{Status: r.status}
}

func TestHeartbeatRejectionBacksOffExponentiallyAndCapsThenClearsOnSuccess(t *testing.T) {
	a, _ := testAgent(t)
	api := &rejectingAPI{status: http.StatusForbidden}
	a.api = api
	ctx := context.Background()

	a.heartbeat(ctx, "start")
	if a.rejectBackoff != heartbeatRejectBackoffMin || api.calls != 1 {
		t.Fatalf("first rejection: backoff=%v calls=%d", a.rejectBackoff, api.calls)
	}
	// Interval ticks inside the backoff window make no network call.
	a.heartbeat(ctx, "interval")
	if api.calls != 1 {
		t.Fatalf("interval tick during backoff must be skipped, calls=%d", api.calls)
	}
	// Direct callers bypass the window and double the backoff.
	a.heartbeat(ctx, "start")
	if a.rejectBackoff != 2*heartbeatRejectBackoffMin {
		t.Fatalf("backoff should double, got %v", a.rejectBackoff)
	}
	for i := 0; i < 10; i++ {
		a.heartbeat(ctx, "start")
	}
	if a.rejectBackoff != heartbeatRejectBackoffMax {
		t.Fatalf("backoff must cap at %v, got %v", heartbeatRejectBackoffMax, a.rejectBackoff)
	}
	if until := time.Until(a.rejectBackoffUntil); until <= 0 || until > heartbeatRejectBackoffMax {
		t.Fatalf("backoff deadline out of range: %v", until)
	}

	api.status = 0
	a.heartbeat(ctx, "start")
	if a.rejectBackoff != 0 || !a.rejectBackoffUntil.IsZero() {
		t.Fatalf("success must clear backoff: %v %v", a.rejectBackoff, a.rejectBackoffUntil)
	}
}

func TestHeartbeatUnauthorizedRecordsRejectionWithoutBackoff(t *testing.T) {
	a, _ := testAgent(t)
	api := &rejectingAPI{status: http.StatusUnauthorized}
	a.api = api
	a.heartbeat(context.Background(), "start")
	if a.credentialRejectedAt.IsZero() || a.rejectBackoff != 0 {
		t.Fatalf("401: rejectedAt=%v backoff=%v", a.credentialRejectedAt, a.rejectBackoff)
	}
	a.heartbeat(context.Background(), "interval")
	if api.calls != 1 {
		t.Fatalf("interval ticks must wait out the credential retry window, calls=%d", api.calls)
	}
}
