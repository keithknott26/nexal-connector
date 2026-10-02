package wol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestParseMAC(t *testing.T) {
	good := map[string]string{
		"aa:bb:cc:dd:ee:ff": "aa:bb:cc:dd:ee:ff", // 0xaa: group bit clear, so unicast
		"3C-22-FB-01-02-03": "3c:22:fb:01:02:03",
		"3c:22:fb:01:02:03": "3c:22:fb:01:02:03",
		"02:00:00:00:00:01": "02:00:00:00:00:01", // locally administered is a valid wake target
	}
	for in, want := range good {
		mac, err := ParseMAC(in)
		if err != nil || mac.String() != want {
			t.Errorf("ParseMAC(%q) = %v, %v; want %s", in, mac, err, want)
		}
	}
	bad := []string{
		"", "aa:bb:cc:dd:ee", "aa:bb:cc:dd:ee:ff:00", "aa:bb-cc:dd:ee:ff", "aa.bb.cc.dd.ee.ff",
		"aabb.ccdd.eeff", "gg:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:f ", " aa:bb:cc:dd:ee:f",
		"00:00:00:00:00:00", // zero
		"ff:ff:ff:ff:ff:ff", // broadcast
		"01:00:5e:00:00:01", // IPv4 multicast
		"33:33:00:00:00:01", // IPv6 multicast
		"aa:bb:cc:dd:ee:ff\n",
	}
	for _, in := range bad {
		if _, err := ParseMAC(in); err == nil {
			t.Errorf("ParseMAC(%q) accepted", in)
		}
	}
}

func TestBuildMagicPacket(t *testing.T) {
	mac, _ := ParseMAC("3c:22:fb:01:02:03")
	p, err := BuildMagicPacket(mac)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 102 {
		t.Fatalf("len = %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) {
		t.Fatal("sync stream is not six 0xFF bytes")
	}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(p[6+i*6:12+i*6], mac) {
			t.Fatalf("repetition %d is wrong", i)
		}
	}
	if _, err := BuildMagicPacket(net.HardwareAddr{1, 2, 3, 4, 5, 6}); err == nil {
		t.Fatal("multicast MAC accepted by BuildMagicPacket")
	}
	if _, err := BuildMagicPacket(net.HardwareAddr{2, 3, 4, 5, 6, 7, 8, 9}); err == nil {
		t.Fatal("EUI-64 accepted by BuildMagicPacket")
	}
}

func TestDirectedBroadcasts(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	in := []ifaceV4{
		{Name: "en0", Flags: up, Prefix: netip.MustParsePrefix("192.168.1.23/24")},
		{Name: "en1", Flags: up, Prefix: netip.MustParsePrefix("10.20.5.9/22")},
		{Name: "en7", Flags: up, Prefix: netip.MustParsePrefix("192.168.1.99/24")},              // same LAN: deduplicated
		{Name: "en2", Flags: net.FlagBroadcast, Prefix: netip.MustParsePrefix("172.16.0.2/16")}, // down
		{Name: "lo0", Flags: up | net.FlagLoopback, Prefix: netip.MustParsePrefix("127.0.0.1/8")},
		{Name: "utun3", Flags: up, Prefix: netip.MustParsePrefix("100.64.0.2/10")},
		{Name: "bridge0", Flags: up, Prefix: netip.MustParsePrefix("192.168.2.1/24")},
		{Name: "en3", Flags: up, Prefix: netip.MustParsePrefix("169.254.10.1/16")},      // link-local
		{Name: "en4", Flags: up, Prefix: netip.MustParsePrefix("192.0.2.1/31")},         // no broadcast
		{Name: "en5", Flags: net.FlagUp, Prefix: netip.MustParsePrefix("192.0.2.9/24")}, // not broadcast-capable
	}
	got := directedBroadcasts(in)
	want := []netip.Addr{netip.MustParseAddr("192.168.1.255"), netip.MustParseAddr("10.20.7.255")}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

type fakeUDP struct {
	sent    []netip.AddrPort
	fail    map[netip.Addr]bool
	packets [][]byte
}

func (f *fakeUDP) WriteToUDPAddrPort(b []byte, a netip.AddrPort) (int, error) {
	if f.fail[a.Addr()] {
		return 0, errors.New("unreachable")
	}
	f.sent = append(f.sent, a)
	f.packets = append(f.packets, append([]byte(nil), b...))
	return len(b), nil
}
func (f *fakeUDP) Close() error { return nil }

func withFakes(t *testing.T, ifaces []ifaceV4, raw []net.Interface, udp *fakeUDP) {
	t.Helper()
	oldIf, oldUDP := systemInterfaces, openUDP
	systemInterfaces = func() ([]ifaceV4, []net.Interface, error) { return ifaces, raw, nil }
	openUDP = func() (udpSender, error) { return udp, nil }
	t.Cleanup(func() { systemInterfaces, openUDP = oldIf, oldUDP })
}

func TestSend(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	udp := &fakeUDP{}
	withFakes(t, []ifaceV4{
		{Name: "en0", Flags: up, Prefix: netip.MustParsePrefix("192.168.1.23/24")},
		{Name: "en1", Flags: up, Prefix: netip.MustParsePrefix("10.0.0.5/8")},
	}, nil, udp)
	a, _ := ParseMAC("3c:22:fb:01:02:03")
	b, _ := ParseMAC("3c:22:fb:01:02:04")
	n, err := Send([]net.HardwareAddr{a, b})
	if err != nil || n != 2 {
		t.Fatalf("Send = %d, %v; want 2 interfaces", n, err)
	}
	// 2 MACs x (2 directed + 1 limited).
	if len(udp.sent) != 6 {
		t.Fatalf("sent %d packets, want 6: %v", len(udp.sent), udp.sent)
	}
	for _, d := range udp.sent {
		if d.Port() != 9 {
			t.Fatalf("sent to port %d, want 9", d.Port())
		}
	}
	if udp.sent[len(udp.sent)-1].Addr() != netip.MustParseAddr("255.255.255.255") {
		t.Fatal("limited broadcast not sent")
	}
	if _, err := Send(nil); err == nil {
		t.Fatal("empty MAC list accepted")
	}
	many := make([]net.HardwareAddr, MaxMACs+1)
	for i := range many {
		many[i] = a
	}
	if _, err := Send(many); err == nil {
		t.Fatal("over-cap MAC list accepted")
	}
}

func TestSendAllFail(t *testing.T) {
	udp := &fakeUDP{fail: map[netip.Addr]bool{netip.MustParseAddr("255.255.255.255"): true}}
	withFakes(t, nil, nil, udp)
	a, _ := ParseMAC("3c:22:fb:01:02:03")
	if _, err := Send([]net.HardwareAddr{a}); err == nil {
		t.Fatal("Send reported success with nothing sent")
	}
}

func TestCollectLocal(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	raw := []net.Interface{
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},
		{Name: "en0", Flags: up, HardwareAddr: net.HardwareAddr{0x3c, 0x22, 0xfb, 1, 2, 3}},
		{Name: "en1", Flags: up, HardwareAddr: net.HardwareAddr{0x3c, 0x22, 0xfb, 1, 2, 4}},
		{Name: "en9", Flags: net.FlagBroadcast, HardwareAddr: net.HardwareAddr{0x3c, 0x22, 0xfb, 1, 2, 5}}, // down: MAC kept
		{Name: "en2", Flags: up, HardwareAddr: net.HardwareAddr{0x3e, 0x22, 0xfb, 1, 2, 6}},                // locally administered
		{Name: "awdl0", Flags: up, HardwareAddr: net.HardwareAddr{0x3c, 0x22, 0xfb, 9, 9, 9}},
		{Name: "bridge0", Flags: up, HardwareAddr: net.HardwareAddr{0x3c, 0x22, 0xfb, 8, 8, 8}},
		{Name: "en3", Flags: up, HardwareAddr: net.HardwareAddr{0, 0, 0, 0, 0, 0}},
		{Name: "utun0", Flags: up},
	}
	v4 := []ifaceV4{
		{Name: "en0", Flags: up, Prefix: netip.MustParsePrefix("192.168.1.23/24")},
		{Name: "en1", Flags: up, Prefix: netip.MustParsePrefix("10.0.4.5/22")},
		{Name: "en2", Flags: up, Prefix: netip.MustParsePrefix("172.20.0.5/16")}, // not physical MAC
		{Name: "bridge0", Flags: up, Prefix: netip.MustParsePrefix("192.168.2.1/24")},
		{Name: "en0", Flags: up, Prefix: netip.MustParsePrefix("169.254.3.3/16")}, // link-local
	}
	withFakes(t, v4, raw, &fakeUDP{})
	f := CollectLocal(context.Background())
	wantMACs := []string{"3c:22:fb:01:02:03", "3c:22:fb:01:02:04", "3c:22:fb:01:02:05"}
	if len(f.MACs) != len(wantMACs) {
		t.Fatalf("MACs = %v, want %v", f.MACs, wantMACs)
	}
	for i := range wantMACs {
		if f.MACs[i] != wantMACs[i] {
			t.Fatalf("MACs = %v, want %v", f.MACs, wantMACs)
		}
	}
	if len(f.Prefixes) != 2 || f.Prefixes[0] != "10.0.4.0/22" || f.Prefixes[1] != "192.168.1.0/24" {
		t.Fatalf("Prefixes = %v", f.Prefixes)
	}
	if f.LANKey != LANKey([]string{"192.168.1.0/24", "10.0.4.0/22"}) || len(f.LANKey) != 64 {
		t.Fatalf("LANKey = %q", f.LANKey)
	}
	if f.WakeForNetwork == "" {
		t.Fatal("wake-for-network must be enabled, disabled or unknown, never empty")
	}
}

func TestLANKey(t *testing.T) {
	a := LANKey([]string{"192.168.1.0/24", "10.0.0.0/8"})
	if a != LANKey([]string{"10.0.0.0/8", "192.168.1.0/24"}) || len(a) != 64 {
		t.Fatal("LANKey depends on enumeration order")
	}
	if a != LANKey([]string{"10.0.0.0/8", "192.168.1.0/24", "10.0.0.0/8"}) {
		t.Fatal("LANKey depends on duplicates")
	}
	// Exact definition: sha256 hex of the sorted prefixes joined by ",", with no
	// public address mixed in (the coordinator salts it with the source IP).
	sum := sha256.Sum256([]byte("10.0.0.0/8,192.168.1.0/24"))
	if a != hex.EncodeToString(sum[:]) {
		t.Fatalf("LANKey = %s, want sha256 of the joined prefixes", a)
	}
	if LANKey([]string{"192.168.1.0/24"}) == LANKey([]string{"192.168.2.0/24"}) {
		t.Fatal("different LANs share a key")
	}
	if LANKey(nil) != "" {
		t.Fatal("no prefixes must yield no key")
	}
}

func TestParseWomp(t *testing.T) {
	const sample = `System-wide power settings:
Currently in use:
 standby              0
 Sleep On Power Button 1
 womp                 1
 autorestart          0
 hibernatefile        /var/vm/sleepimage
`
	if ParseWomp([]byte(sample)) != WakeEnabled {
		t.Fatal("womp 1 not read as enabled")
	}
	if ParseWomp([]byte(" womp                 0\n")) != WakeDisabled {
		t.Fatal("womp 0 not read as disabled")
	}
	for _, s := range []string{"", " standby 0\n", " womp 1\n womp 0\n", " womp 2\n", " wompx 1\n", "womp 1 extra\n"} {
		if ParseWomp([]byte(s)) != WakeUnknown {
			t.Errorf("ParseWomp(%q) was not unknown", s)
		}
	}
}

func TestPrefixKeysShareAnyPrefix(t *testing.T) {
	home := PrefixKeys([]string{"192.168.1.0/24"})
	multi := PrefixKeys([]string{"10.0.4.0/22", "192.168.1.0/24", "192.168.1.0/24"})
	if len(home) != 1 || len(multi) != 2 {
		t.Fatalf("PrefixKeys sizes = %d, %d", len(home), len(multi))
	}
	shared := false
	for _, k := range multi {
		if k == home[0] {
			shared = true
		}
		if len(k) != 64 {
			t.Fatalf("key %q is not a hex sha256", k)
		}
	}
	if !shared {
		t.Fatal("a Mac with an extra prefix must still share the home prefix key")
	}
	if PrefixKeys(nil) == nil || len(PrefixKeys(nil)) != 0 {
		t.Fatal("PrefixKeys(nil) must be an empty, non-nil slice")
	}
	if home[0] == LANKey([]string{"192.168.1.0/24"}) {
		t.Fatal("prefix keys must be domain-separated from LANKey")
	}
}
