package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"nexal/connector/internal/client"
	"nexal/connector/internal/wol"
)

type fakeRelay struct {
	pending []client.WakeRequest
	acked   []string
}

func (f *fakeRelay) PendingWakes(context.Context, string) ([]client.WakeRequest, error) {
	return f.pending, nil
}
func (f *fakeRelay) WakeSent(_ context.Context, _ string, id string) error {
	f.acked = append(f.acked, id)
	return nil
}

func TestRelayWakesBroadcastsAndAcknowledges(t *testing.T) {
	prev := sendMagicPacket
	defer func() { sendMagicPacket = prev }()
	var sent []string
	sendMagicPacket = func(mac, broadcast string) error { sent = append(sent, mac+"@"+broadcast); return nil }
	b := "192.168.68.255"
	f := &fakeRelay{pending: []client.WakeRequest{{ID: "wake_1", MAC: "a4:83:e7:12:34:56", Broadcast: &b}}}
	a := &Agent{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.relayWakes(context.Background(), f, "host_1")
	if len(sent) != 1 || sent[0] != "a4:83:e7:12:34:56@192.168.68.255" || len(f.acked) != 1 || f.acked[0] != "wake_1" {
		t.Fatalf("sent %v acked %v", sent, f.acked)
	}
}

func TestWakeInfo(t *testing.T) {
	prev := localWakeInterface
	defer func() { localWakeInterface = prev }()
	localWakeInterface = func() (wol.Interface, bool) {
		return wol.Interface{MAC: "a4:83:e7:12:34:56", Broadcast: "192.168.68.255"}, true
	}
	w := wakeInfo("100.113.174.101")
	if w == nil || w.MAC != "a4:83:e7:12:34:56" || w.Tunnel != "100.113.174.101" {
		t.Fatalf("%+v", w)
	}
	localWakeInterface = func() (wol.Interface, bool) { return wol.Interface{}, false }
	if wakeInfo("192.168.1.2") != nil {
		t.Fatal("non-tunnel address must not be reported")
	}
}
