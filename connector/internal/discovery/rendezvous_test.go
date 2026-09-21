package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexal/connector/internal/client"
)

type fakeSource struct {
	directory client.PeerDirectory
	err       error
	calls     int
}

func (f *fakeSource) Peers(context.Context) (client.PeerDirectory, error) {
	f.calls++
	if f.err != nil {
		return client.PeerDirectory{}, f.err
	}
	return f.directory, nil
}

func directoryWith(peers ...client.DirectoryPeer) client.PeerDirectory {
	return client.PeerDirectory{
		Peers:              peers,
		Authorization:      client.AuthorizationCandidateList,
		CapabilityEvidence: client.CapabilityEvidenceSelfReported,
	}
}

func newTestRendezvous(t *testing.T, source PeerSource, now *time.Time, maxStale time.Duration) *Rendezvous {
	t.Helper()
	r, err := NewRendezvous(RendezvousOptions{
		Source:       source,
		Sharing:      SharingPolicy{WANRendezvous: true},
		Interval:     30 * time.Second,
		StaleAfter:   90 * time.Second,
		MaxStaleness: maxStale,
		Clock:        func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRendezvousIsGatedOffByDefault(t *testing.T) {
	if _, err := NewRendezvous(RendezvousOptions{Source: &fakeSource{}, Sharing: DefaultSharing()}); err != ErrSharingDisabled {
		t.Fatal("rendezvous polling is not gated off by default")
	}
	if _, err := NewRendezvous(RendezvousOptions{Sharing: SharingPolicy{WANRendezvous: true}}); err != ErrInvalid {
		t.Fatal("accepted a nil source")
	}
	if _, err := NewRendezvous(RendezvousOptions{Source: &fakeSource{},
		Sharing: SharingPolicy{WANRendezvous: true}, StaleAfter: time.Minute, MaxStaleness: time.Second}); err != ErrInvalid {
		t.Fatal("accepted a MaxStaleness shorter than StaleAfter")
	}
}

func TestRendezvousBeforeItsFirstPollAuthorizesNobody(t *testing.T) {
	now := time.Unix(1700000000, 0)
	r := newTestRendezvous(t, &fakeSource{}, &now, 0)
	s := r.Snapshot()
	if s.Ready() || len(s.Peers) != 0 || s.Stale {
		t.Fatalf("snapshot before the first poll: %+v", s)
	}
	if got := r.AllowedPeers(); len(got) != 0 {
		t.Fatalf("AllowedPeers = %v before any coordinator read", got)
	}
}

func TestRendezvousPollPopulatesTheAuthorizedSet(t *testing.T) {
	now := time.Unix(1700000000, 0)
	source := &fakeSource{directory: directoryWith(client.DirectoryPeer{
		HostID: "mac2", Name: "Studio", Fingerprint: fpPeer,
		Addresses: []client.PeerAddress{
			{Kind: "lan", Address: "192.168.4.7", Port: 8443},
			{Kind: "wan", Address: "93.184.216.34", Port: 8443},
		},
		SelfReportedCapabilities: client.PeerCapabilities{Chip: "M4 Pro", ThunderboltGeneration: 5},
	})}
	r := newTestRendezvous(t, source, &now, 0)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if !s.Ready() || s.Stale || len(s.Peers) != 1 || s.Failures != 0 {
		t.Fatalf("snapshot after a good poll: %+v", s)
	}
	if len(s.Peers[0].Addresses) != 2 || s.Peers[0].SelfReportedCapabilities.Chip != "M4 Pro" {
		t.Fatalf("peer decoded as %+v", s.Peers[0])
	}
	if got := r.AllowedPeers(); len(got) != 1 || got[0] != fpPeer {
		t.Fatalf("AllowedPeers = %v", got)
	}
	// A row the coordinator sends that cannot be pinned is skipped, not fatal:
	// one bad row must not delete a working allowlist.
	source.directory = directoryWith(
		client.DirectoryPeer{HostID: "mac2", Fingerprint: fpPeer},
		client.DirectoryPeer{HostID: "mac4", Fingerprint: "unpinnable"},
	)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.AllowedPeers(); len(got) != 1 || got[0] != fpPeer {
		t.Fatalf("AllowedPeers = %v after a malformed row", got)
	}
}

// TestCoordinatorUnavailabilityKeepsTheLastKnownSet is the behaviour §4.3 asks
// for: keep what we knew, say how old it is, never fail open and never empty the
// allowlist underneath a transfer.
func TestCoordinatorUnavailabilityKeepsTheLastKnownSet(t *testing.T) {
	now := time.Unix(1700000000, 0)
	source := &fakeSource{directory: directoryWith(client.DirectoryPeer{HostID: "mac2", Fingerprint: fpPeer})}
	r := newTestRendezvous(t, source, &now, 0)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	observed := r.Snapshot().ObservedAt

	source.err = errors.New("coordinator request failed [network]: connection failed")
	now = now.Add(2 * time.Minute)
	for range 3 {
		if err := r.Refresh(context.Background()); err == nil {
			t.Fatal("a failed poll reported success")
		}
	}
	s := r.Snapshot()
	if len(s.Peers) != 1 || s.Peers[0].Fingerprint != fpPeer {
		t.Fatalf("the last known set was not retained: %+v", s)
	}
	if !s.Stale || s.Failures != 3 || s.LastError == "" {
		t.Fatalf("staleness was not reported: %+v", s)
	}
	if !s.ObservedAt.Equal(observed) {
		t.Fatal("ObservedAt moved on a failed poll")
	}
	if s.Age != 2*time.Minute {
		t.Fatalf("age is %s", s.Age)
	}
	if got := r.AllowedPeers(); len(got) != 1 {
		t.Fatalf("AllowedPeers emptied during an outage: %v", got)
	}
	// Recovery clears the staleness and the error, without a restart.
	source.err = nil
	now = now.Add(time.Second)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); s.Stale || s.Failures != 0 || s.LastError != "" {
		t.Fatalf("snapshot after recovery: %+v", s)
	}
}

func TestSuccessfulEmptyPollDoesEmptyTheSet(t *testing.T) {
	now := time.Unix(1700000000, 0)
	source := &fakeSource{directory: directoryWith(client.DirectoryPeer{HostID: "mac2", Fingerprint: fpPeer})}
	r := newTestRendezvous(t, source, &now, 0)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The coordinator answering "no peers" is an answer, not an outage: that is
	// how a revocation or a removal actually takes effect here.
	source.directory = directoryWith()
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.AllowedPeers(); len(got) != 0 {
		t.Fatalf("AllowedPeers = %v after the coordinator listed no peers", got)
	}
}

func TestMaxStalenessDropsTheRetainedSet(t *testing.T) {
	now := time.Unix(1700000000, 0)
	source := &fakeSource{directory: directoryWith(client.DirectoryPeer{HostID: "mac2", Fingerprint: fpPeer})}
	r := newTestRendezvous(t, source, &now, 10*time.Minute)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.err = errors.New("coordinator request failed [timeout]: request timed out")
	now = now.Add(9 * time.Minute)
	_ = r.Refresh(context.Background())
	if s := r.Snapshot(); s.Expired || len(s.Peers) != 1 {
		t.Fatalf("set expired before MaxStaleness: %+v", s)
	}
	now = now.Add(2 * time.Minute)
	s := r.Snapshot()
	if !s.Expired || len(s.Peers) != 0 || !s.Stale {
		t.Fatalf("set did not expire after MaxStaleness: %+v", s)
	}
	if got := r.AllowedPeers(); len(got) != 0 {
		t.Fatalf("AllowedPeers = %v after expiry", got)
	}
}

func TestSnapshotIgnoresABackwardClock(t *testing.T) {
	now := time.Unix(1700000000, 0)
	source := &fakeSource{directory: directoryWith(client.DirectoryPeer{HostID: "mac2", Fingerprint: fpPeer})}
	r := newTestRendezvous(t, source, &now, 0)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Hour)
	if s := r.Snapshot(); s.Age != 0 || s.Stale {
		t.Fatalf("a clock that moved backwards produced %+v", s)
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	now := time.Unix(1700000000, 0)
	source := &fakeSource{directory: directoryWith(client.DirectoryPeer{HostID: "mac2", Fingerprint: fpPeer})}
	r := newTestRendezvous(t, source, &now, 0)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	s.Peers[0].Fingerprint = fpThird
	if got := r.AllowedPeers(); len(got) != 1 || got[0] != fpPeer {
		t.Fatalf("a caller mutated the retained set: %v", got)
	}
}

func TestPollWaitIsJitteredAndBacksOff(t *testing.T) {
	now := time.Unix(1700000000, 0)
	source := &fakeSource{err: errors.New("coordinator request failed [network]: connection failed")}
	r := newTestRendezvous(t, source, &now, 0)
	var distinct = map[time.Duration]bool{}
	for range 32 {
		w := r.wait()
		if w < 24*time.Second || w > 36*time.Second {
			t.Fatalf("wait %s outside the jitter band around 30s", w)
		}
		distinct[w] = true
	}
	if len(distinct) < 8 {
		t.Fatalf("only %d distinct wait values: polls are not decorrelated", len(distinct))
	}
	// Repeated failure backs off, so an outage does not become a thundering herd.
	for range 3 {
		_ = r.Refresh(context.Background())
	}
	if w := r.wait(); w < 3*time.Minute {
		t.Fatalf("wait after 3 failures is %s, expected a backoff", w)
	}
}

func TestRunPollsAndStopsWithTheContext(t *testing.T) {
	now := time.Now()
	source := &fakeSource{directory: directoryWith(client.DirectoryPeer{HostID: "mac2", Fingerprint: fpPeer})}
	r := newTestRendezvous(t, source, &now, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	deadline := time.After(5 * time.Second)
	for {
		if r.Snapshot().Ready() {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run did not poll immediately")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}
