package discovery

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

// TestListenBrowseAndCloseOnRealSockets exercises the socket path — the group
// joins, the read loop, the query write and shutdown — rather than only the
// pure decode and merge logic.
//
// It skips rather than fails when the environment refuses multicast: a container
// with no multicast-capable interface, or a Mac on a full-tunnel VPN that has
// captured the default route, are both expected states and neither is a defect in
// this package. What the test does assert is that a refusal is reported as an
// error instead of a half-initialised listener, and that Run returns when its
// context is cancelled.
func TestListenBrowseAndCloseOnRealSockets(t *testing.T) {
	ad := Advertisement{
		HostID: "mac1", Fingerprint: fpSelf, Version: "0.1.0", Port: 8443,
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.5")},
	}
	m, err := Listen(Options{Advertisement: ad, Sharing: SharingPolicy{LANDiscovery: true}})
	if err != nil {
		t.Skipf("multicast unavailable in this environment: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	candidates := make(chan []Candidate, 8)
	go func() {
		defer close(stopped)
		m.Run(ctx, func(c []Candidate) {
			select {
			case candidates <- c:
			default:
			}
		})
	}()
	// Give the read loops a moment to reach their first read before querying.
	time.Sleep(100 * time.Millisecond)
	if err := m.Browse(); err != nil {
		cancel()
		<-stopped
		t.Skipf("multicast send refused in this environment: %v", err)
	}
	// Any candidate that does arrive must already have passed validation; an
	// empty result is the normal outcome when no other Nexal host is present.
	select {
	case got := <-candidates:
		for _, c := range got {
			if !ValidFingerprint(c.Fingerprint) || !ValidHostID(c.HostID) || c.Port == 0 {
				t.Fatalf("unvalidated candidate reached the sink: %+v", c)
			}
		}
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	// Close is idempotent, and Browse after Close reports a closed listener
	// rather than writing to a closed socket.
	m.Close()
	if err := m.Browse(); err == nil {
		t.Fatal("Browse succeeded after Close")
	}
}
