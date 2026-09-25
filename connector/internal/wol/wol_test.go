package wol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"
)

func TestMagicPacket(t *testing.T) {
	mac, _ := net.ParseMAC("a4:83:e7:12:34:56")
	p, err := MagicPacket(mac)
	if err != nil || len(p) != 102 || !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) || !bytes.Equal(p[96:], mac) {
		t.Fatalf("packet %x err %v", p, err)
	}
}

func hw(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }

const lan = net.FlagUp | net.FlagBroadcast | net.FlagMulticast

func fakeInterfaces(t *testing.T, list []iface) {
	prev := localInterfaces
	t.Cleanup(func() { localInterfaces = prev })
	localInterfaces = func() ([]iface, error) { return list, nil }
}

func pfx(ss ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func typicalMac(t *testing.T) {
	fakeInterfaces(t, []iface{
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback, Addrs: pfx("127.0.0.1/8")},
		{Name: "en0", Flags: lan, MAC: hw("A4:83:E7:12:34:56"), Addrs: pfx("192.168.68.51/22", "fe80::1/64")},
		{Name: "en1", Flags: lan, MAC: hw("3c:22:fb:00:00:01"), Addrs: pfx("192.168.68.60/22", "10.1.2.3/24")},
		{Name: "en5", Flags: lan, MAC: hw("3c:22:fb:00:00:09"), Addrs: pfx("169.254.3.4/16")},            // link-local only
		{Name: "en6", Flags: net.FlagBroadcast, MAC: hw("3c:22:fb:00:00:0a"), Addrs: pfx("10.9.9.9/24")}, // down
		{Name: "en7", Flags: lan, MAC: hw("3c:22:fb:00:00:0b"), Addrs: pfx("8.8.8.8/24")},                // public
		{Name: "utun3", Flags: net.FlagUp | net.FlagPointToPoint, Addrs: pfx("100.113.174.101/16")},
		{Name: "bridge100", Flags: lan, MAC: hw("3e:22:fb:00:00:64"), Addrs: pfx("192.168.64.1/24")}, // VM bridge
		{Name: "en8", Flags: lan, MAC: hw("3c:22:fb:00:00:0c"), Addrs: pfx("100.64.1.1/24")},         // CGNAT is not private
	})
}

func TestWakeInterfaces(t *testing.T) {
	typicalMac(t)
	macs, key := WakeInterfaces()
	if !reflect.DeepEqual(macs, []string{"3c:22:fb:00:00:01", "a4:83:e7:12:34:56"}) {
		t.Fatalf("macs %v", macs)
	}
	sum := sha256.Sum256([]byte("10.1.2.0/24\n192.168.68.0/22"))
	if key != hex.EncodeToString(sum[:]) {
		t.Fatalf("lanKey %s", key)
	}

	// No private network → nothing to report.
	fakeInterfaces(t, []iface{{Name: "en0", Flags: lan, MAC: hw("a4:83:e7:12:34:56"), Addrs: pfx("8.8.8.8/24")}})
	if macs, key := WakeInterfaces(); macs != nil || key != "" {
		t.Fatalf("%v %q", macs, key)
	}
	// Multicast / zero hardware addresses are not wakeable.
	fakeInterfaces(t, []iface{{Name: "en0", Flags: lan, MAC: hw("01:00:5e:00:00:01"), Addrs: pfx("10.0.0.2/8")},
		{Name: "en1", Flags: lan, MAC: hw("00:00:00:00:00:00"), Addrs: pfx("10.0.0.3/8")}})
	if macs, _ := WakeInterfaces(); macs != nil {
		t.Fatalf("%v", macs)
	}
	// At most MaxMACs, deterministic.
	var many []iface
	for i := 0; i < 12; i++ {
		many = append(many, iface{Name: "en" + string(rune('a'+i)), Flags: lan, MAC: net.HardwareAddr{0x3c, 0, 0, 0, 0, byte(12 - i)}, Addrs: pfx("10.0.0.2/8")})
	}
	fakeInterfaces(t, many)
	if macs, _ := WakeInterfaces(); len(macs) != MaxMACs || macs[0] != "3c:00:00:00:00:01" {
		t.Fatalf("%v", macs)
	}
	fakeInterfaces(t, nil)
	prev := localInterfaces
	localInterfaces = func() ([]iface, error) { return nil, errors.New("boom") }
	defer func() { localInterfaces = prev }()
	if macs, key := WakeInterfaces(); macs != nil || key != "" {
		t.Fatal("error must yield nothing")
	}
}

func TestSendAllTargets(t *testing.T) {
	typicalMac(t)
	var got []string
	var packets [][]byte
	prev := sendTo
	defer func() { sendTo = prev }()
	sendTo = func(p []byte, addr string) error { got = append(got, addr); packets = append(packets, p); return nil }
	if err := SendAll([]string{"A4:83:E7:12:34:56", "bogus", "3c:22:fb:00:00:02"}); err != nil {
		t.Fatal(err)
	}
	dests := []string{"255.255.255.255", "192.168.71.255", "10.1.2.255"}
	var want []string
	for range 2 {
		for _, d := range dests {
			want = append(want, d+":9", d+":7")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if !bytes.Equal(packets[0][6:12], hw("a4:83:e7:12:34:56")) || !bytes.Equal(packets[len(packets)-1][6:12], hw("3c:22:fb:00:00:02")) {
		t.Fatal("wrong packet contents")
	}
	for _, a := range got {
		if a == "192.168.68.51:9" || a == "192.168.64.255:9" || a == "100.64.1.255:9" {
			t.Fatalf("unexpected destination %s", a)
		}
	}
	if SendAll([]string{"nope", "01:00:5e:00:00:01"}) == nil {
		t.Fatal("no valid MAC accepted")
	}
	sendTo = func([]byte, string) error { return errors.New("no route") }
	if SendAll([]string{"a4:83:e7:12:34:56"}) == nil {
		t.Fatal("total failure reported as success")
	}
	// Partial failure still succeeds.
	n := 0
	sendTo = func([]byte, string) error {
		n++
		if n == 1 {
			return errors.New("x")
		}
		return nil
	}
	if err := SendAll([]string{"a4:83:e7:12:34:56"}); err != nil {
		t.Fatal(err)
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

func TestParseWakeOnMagicPacket(t *testing.T) {
	both := "Battery Power:\n Sleep On Power Button 1\n womp                 1\n sleep 1\nAC Power:\n womp                 1\n"
	cases := map[string]bool{
		both:                                   true,
		"AC Power:\n womp                 1\n": true,
		"Battery Power:\n womp 0\nAC Power:\n womp 1\n": false,
		"AC Power:\n sleep 1\n":                         false,
		"":                                              false,
		" womp 1 extra\n":                               false,
		" wompx 1\n":                                    false,
	}
	for in, want := range cases {
		if got := parseWakeOnMagicPacket(in); got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
}
