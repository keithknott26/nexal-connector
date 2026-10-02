package conn

import "testing"

// TestNexalSendEmptyBatch: WireGuard can hand the bind a zero-length batch. On
// Linux golang.org/x/net's sendmmsg indexed element 0 of it and panicked
// ("index out of range [0] with length 0"), killing the whole mesh daemon on
// the storage gateway. Sending nothing must be a no-op, on every platform.
func TestNexalSendEmptyBatch(t *testing.T) {
	bind := NewStdNetBind()
	if _, _, err := bind.Open(0); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer bind.Close()
	ep, err := bind.ParseEndpoint("127.0.0.1:9")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	for _, bufs := range [][][]byte{nil, {}} {
		if err := bind.Send(bufs, ep); err != nil {
			t.Fatalf("empty batch must be a no-op, got %v", err)
		}
	}
}
