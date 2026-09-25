package wol

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
)

func TestMagicPacket(t *testing.T) {
	mac, _ := net.ParseMAC("a4:83:e7:12:34:56")
	p, err := MagicPacket(mac)
	if err != nil || len(p) != 102 || !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) || !bytes.Equal(p[96:], mac) {
		t.Fatalf("packet %x err %v", p, err)
	}
}

func TestSendTargetsSubnetAndLimitedBroadcast(t *testing.T) {
	var got []string
	prev := sendTo
	defer func() { sendTo = prev }()
	sendTo = func(_ []byte, addr string) error { got = append(got, addr); return nil }
	if err := Send("A4:83:E7:12:34:56", "192.168.68.255"); err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.68.255:9", "192.168.68.255:7", "255.255.255.255:9", "255.255.255.255:7"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	got = nil
	_ = Send("a4:83:e7:12:34:56", "8.8.8.255") // public broadcast ignored
	if len(got) != 2 {
		t.Fatalf("public broadcast must be ignored: %v", got)
	}
	if Send("nope", "") == nil {
		t.Fatal("invalid MAC accepted")
	}
}

func TestBroadcastOf(t *testing.T) {
	if b := broadcastOf(netip.MustParsePrefix("192.168.68.51/22")); b != "192.168.71.255" {
		t.Fatal(b)
	}
	if b := broadcastOf(netip.MustParsePrefix("10.0.0.5/8")); b != "10.255.255.255" {
		t.Fatal(b)
	}
}
