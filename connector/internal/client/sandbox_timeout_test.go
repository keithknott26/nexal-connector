package client

import (
	"net/http"
	"testing"
	"time"

	"nexal/connector/internal/observability"
)

// withLongTimeout must widen the limits on a copy only: the shared client keeps its
// ordinary limits, and the copy still uses a cloned transport rather than the original.
func TestWithLongTimeoutCopiesAndDoesNotMutateOriginal(t *testing.T) {
	c, err := New("https://example.invalid", "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	origTimeout := c.http.Timeout
	origTransport, ok := observability.Underlying(c.http.Transport).(*http.Transport)
	if !ok {
		t.Skip("client transport is not an *http.Transport")
	}
	origHeader := origTransport.ResponseHeaderTimeout

	long := c.withLongTimeout(20 * time.Second)

	if long == c || long.http == c.http {
		t.Fatal("withLongTimeout must return a distinct client and http.Client")
	}
	if long.http.Timeout != 20*time.Second {
		t.Fatalf("copy Timeout = %v, want 20s", long.http.Timeout)
	}
	lt, ok := observability.Underlying(long.http.Transport).(*http.Transport)
	if !ok || lt == origTransport {
		t.Fatal("copy must use a cloned *http.Transport")
	}
	if lt.ResponseHeaderTimeout != 20*time.Second {
		t.Fatalf("copy ResponseHeaderTimeout = %v, want 20s", lt.ResponseHeaderTimeout)
	}
	if c.http.Timeout != origTimeout || origTransport.ResponseHeaderTimeout != origHeader {
		t.Fatal("original client's limits were modified")
	}
}
