package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/mesh"
)

const testLANKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeWakeInfoAPI struct {
	puts []client.WakeInfo
	err  error
}

func (f *fakeWakeInfoAPI) PutWakeInfo(_ context.Context, w client.WakeInfo) error {
	f.puts = append(f.puts, w)
	return f.err
}

type tunnelProvider struct{ addr string }

func (p tunnelProvider) Snapshot() mesh.Status { return mesh.Status{SelfTunnelAddress: p.addr} }

func TestPublishWakeInfo(t *testing.T) {
	prevI, prevW, prevR := wakeInterfaces, wakeForNetwork, wakeInfoRefresh
	defer func() { wakeInterfaces, wakeForNetwork, wakeInfoRefresh = prevI, prevW, prevR }()
	macs := []string{"a4:83:e7:12:34:56"}
	key := testLANKey
	wakeInterfaces = func() ([]string, string) { return macs, key }
	wake := true
	wakeForNetwork = func() bool { return wake }

	a := quietAgent()
	a.meshProvider = tunnelProvider{addr: "100.113.174.101"}
	f := &fakeWakeInfoAPI{}
	var st wakeInfoState
	ctx := context.Background()

	a.publishWakeInfo(ctx, f, &st)
	want := client.WakeInfo{MACs: macs, LANKey: key, WakeForNetwork: true, TunnelAddress: "100.113.174.101"}
	if len(f.puts) != 1 || !sameWakeInfo(f.puts[0], want) {
		t.Fatalf("%+v", f.puts)
	}
	// Unchanged within the refresh window: nothing sent.
	a.publishWakeInfo(ctx, f, &st)
	if len(f.puts) != 1 {
		t.Fatalf("resent unchanged: %d", len(f.puts))
	}
	// A change is sent at once.
	wake = false
	a.publishWakeInfo(ctx, f, &st)
	if len(f.puts) != 2 || f.puts[1].WakeForNetwork {
		t.Fatalf("%+v", f.puts)
	}
	// Refresh elapsed: unchanged details are re-sent.
	st.lastAt = time.Now().Add(-wakeInfoRefresh - time.Second)
	a.publishWakeInfo(ctx, f, &st)
	if len(f.puts) != 3 {
		t.Fatalf("not refreshed: %d", len(f.puts))
	}
	// A failed PUT is retried on the next tick even though nothing changed.
	macs = []string{"a4:83:e7:12:34:56", "3c:22:fb:00:00:01"}
	f.err = errors.New("coordinator rejected request (HTTP 503)")
	a.publishWakeInfo(ctx, f, &st)
	a.publishWakeInfo(ctx, f, &st)
	f.err = nil
	a.publishWakeInfo(ctx, f, &st)
	if len(f.puts) != 6 || len(f.puts[5].MACs) != 2 {
		t.Fatalf("%d", len(f.puts))
	}
	a.publishWakeInfo(ctx, f, &st)
	if len(f.puts) != 6 {
		t.Fatal("resent after successful retry")
	}
	// A non-tunnel self address is omitted rather than sent.
	a.meshProvider = tunnelProvider{addr: "192.168.1.5"}
	a.publishWakeInfo(ctx, f, &st)
	if len(f.puts) != 7 || f.puts[6].TunnelAddress != "" {
		t.Fatalf("%+v", f.puts[len(f.puts)-1])
	}
	// No MACs (or no private network): skip entirely.
	macs = nil
	a.publishWakeInfo(ctx, f, &st)
	macs, key = []string{"a4:83:e7:12:34:56"}, ""
	a.publishWakeInfo(ctx, f, &st)
	if len(f.puts) != 7 {
		t.Fatal("published without wakeable interface")
	}
}

func TestRunWakeInfoPublishesAtStartupAndStops(t *testing.T) {
	prevI, prevW := wakeInterfaces, wakeForNetwork
	defer func() { wakeInterfaces, wakeForNetwork = prevI, prevW }()
	wakeInterfaces = func() ([]string, string) { return []string{"a4:83:e7:12:34:56"}, testLANKey }
	wakeForNetwork = func() bool { return false }
	a := quietAgent()
	a.meshProvider = mesh.UnavailableProvider{}
	f := &fakeWakeInfoAPI{}
	a.api = struct {
		*fakeAPI
		*fakeWakeInfoAPI
	}{&fakeAPI{}, f}
	// Already cancelled: the startup publish still happens (the fake ignores
	// ctx), and the loop then returns instead of ticking.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { a.runWakeInfo(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeInfo did not stop")
	}
	if len(f.puts) != 1 || f.puts[0].TunnelAddress != "" {
		t.Fatalf("%+v", f.puts)
	}
}
