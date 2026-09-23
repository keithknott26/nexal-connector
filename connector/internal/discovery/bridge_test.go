package discovery

import (
	"testing"
	"time"
)

func TestBridgeRecordAcceptsOnlyScopedBoundedSMB(t *testing.T) {
	r := BridgeRecord{Service: SMBService, Instance: "Studio", Target: "studio.network.mesh.nexal.systems", Port: 445,
		OriginID: "device-a", SiteID: "site-a", NetworkID: "network-a", Sequence: 2, HopLimit: 1, TTL: 60}
	if err := r.Validate("network-a", 1); err != nil {
		t.Fatal(err)
	}
	if r.DedupKey() == "" {
		t.Fatal("missing dedup key")
	}

	cases := []BridgeRecord{r, r, r, r, r}
	cases[0].Service = "_ssh._tcp"
	cases[1].NetworkID = "network-b"
	cases[2].Sequence = 1
	cases[3].HopLimit = 3
	cases[4].TTL = 121
	for i, candidate := range cases {
		if err := candidate.Validate("network-a", 1); err == nil {
			t.Fatalf("case %d unexpectedly accepted", i)
		}
	}
	rfb := r
	rfb.Service, rfb.Port, rfb.Sequence = ScreenSharingService, 5900, 3
	if err := rfb.Validate("network-a", 2); err != nil {
		t.Fatal(err)
	}
}

func TestReceiverRejectsReplayAndDeduplicatesUntilLeaseExpires(t *testing.T) {
	now := time.Now().UTC()
	r := BridgeRecord{Service: SMBService, Instance: "Studio", Target: "studio.network.mesh.nexal.systems", Port: 445,
		OriginID: "device-a", SiteID: "site-a", NetworkID: "network-a", Sequence: 1, HopLimit: 1, TTL: 10}
	receiver := NewReceiver("network-a")
	if err := receiver.Accept(r, now); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Accept(r, now.Add(time.Second)); err == nil {
		t.Fatal("replay accepted")
	}
	r.Sequence = 2
	if err := receiver.Accept(r, now.Add(time.Second)); err == nil {
		t.Fatal("dedup lease bypassed")
	}
	if err := receiver.Accept(r, now.Add(11*time.Second)); err != nil {
		t.Fatalf("expired lease did not clear: %v", err)
	}
	r.Sequence = 3
	r.Target = "device.netbird.cloud"
	if err := receiver.Accept(r, now.Add(22*time.Second)); err == nil {
		t.Fatal("upstream hostname accepted")
	}
}
