package wol

import (
	"net"
	"net/netip"
	"testing"
)

func TestBroadcastOf(t *testing.T) {
	cases := map[string]string{
		"192.168.1.23/24": "192.168.1.255",
		"10.1.2.3/8":      "10.255.255.255",
		"172.16.5.9/20":   "172.16.15.255",
		"192.168.1.5/30":  "192.168.1.7",
		"192.168.1.5/31":  "192.168.1.5",
		"192.168.1.5/32":  "192.168.1.5",
		"1.2.3.4/0":       "255.255.255.255",
	}
	for in, want := range cases {
		if got := broadcastOf(netip.MustParsePrefix(in)); got.String() != want {
			t.Errorf("broadcastOf(%s) = %s; want %s", in, got, want)
		}
	}
}

func TestIsVirtual(t *testing.T) {
	for _, n := range []string{"utun3", "awdl0", "llw0", "bridge100", "lo0", "wt0", "anpi0", "ap1",
		"gif0", "stf0", "ipsec0", "ppp0", "vmenet0", "feth1", "tun0", "tap0"} {
		if !isVirtual(n) {
			t.Errorf("isVirtual(%q) = false; want true", n)
		}
	}
	for _, n := range []string{"en0", "en7", "", "eth0"} {
		if isVirtual(n) {
			t.Errorf("isVirtual(%q) = true; want false", n)
		}
	}
}

func TestPhysicalMAC(t *testing.T) {
	good := []string{"3c:22:fb:01:02:03", "a8:bb:cc:dd:ee:ff"}
	for _, s := range good {
		hw, _ := net.ParseMAC(s)
		if !physicalMAC(hw) {
			t.Errorf("physicalMAC(%s) = false; want true", s)
		}
	}
	bad := []string{
		"02:00:00:00:00:01", // locally administered
		"da:a1:19:00:00:01", // randomized Wi-Fi (0xda sets the local bit)
		"01:00:5e:00:00:01", // multicast
		"ff:ff:ff:ff:ff:ff", // broadcast
		"00:00:00:00:00:00", // zero
	}
	for _, s := range bad {
		hw, _ := net.ParseMAC(s)
		if physicalMAC(hw) {
			t.Errorf("physicalMAC(%s) = true; want false", s)
		}
	}
	if physicalMAC(nil) || physicalMAC(net.HardwareAddr{1, 2, 3}) {
		t.Error("physicalMAC accepted a wrong-length address")
	}
}
