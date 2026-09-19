package lab

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestHardwarePortMappingDoesNotAssumeEnZeroIsWired(t *testing.T) {
	ports := parseHardwarePorts(`Hardware Port: Wi-Fi
Device: en0
Ethernet Address: aa:bb:cc:dd:ee:ff

Hardware Port: USB 10/100/1000 LAN
Device: en7
Ethernet Address: 00:11:22:33:44:55

Hardware Port: Thunderbolt Bridge
Device: bridge0
`)
	for _, tc := range []struct{ device, want string }{
		{"en0", "Wi-Fi"}, {"en7", "Ethernet (wired)"}, {"bridge0", "Thunderbolt bridge"},
		{"en1", "Unknown connection type"},
	} {
		if got := connectionType(tc.device, ports[tc.device]); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.device, got, tc.want)
		}
	}
}

func TestVirtualAndUnknownConnectionLabels(t *testing.T) {
	for _, tc := range []struct{ device, port, want string }{
		{"utun4", "Ethernet", "VPN/tunnel (not verified LAN)"},
		{"tap0", "", "VPN/tunnel (not verified LAN)"},
		{"awdl0", "", "Apple peer-to-peer (not ordinary Wi-Fi LAN)"},
		{"bridge2", "", "Bridge/virtual (type unverified)"},
		{"en0", "", "Unknown connection type"},
		{"en4", "AirPort", "Wi-Fi"},
		{"en6", "Thunderbolt Ethernet", "Ethernet (wired)"},
	} {
		if got := connectionType(tc.device, tc.port); got != tc.want {
			t.Fatalf("%+v: %q", tc, got)
		}
	}
}

func TestCollectActivePrivateNetworksSortedAndLabeled(t *testing.T) {
	ifaces := []net.Interface{
		{Name: "en7", Flags: net.FlagUp}, {Name: "en0", Flags: net.FlagUp},
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback}, {Name: "en9"},
	}
	read := func(i net.Interface) ([]net.Addr, error) {
		if i.Name == "lo0" || i.Name == "en9" {
			t.Fatal("down/loopback interface should not be inspected")
		}
		cidrs := []string{"192.168.1.20/24", "192.168.1.20/24", "8.8.8.8/24", "169.254.1.2/16", "fe80::1/64"}
		if i.Name == "en0" {
			cidrs = []string{"192.168.1.30/24", "fd00::30/64"}
		}
		var out []net.Addr
		for _, c := range cidrs {
			ip, network, err := net.ParseCIDR(c)
			if err != nil {
				t.Fatal(err)
			}
			network.IP = ip
			out = append(out, network)
		}
		return out, nil
	}
	rows, err := collectNetworks(ifaces, read, map[string]string{"en0": "Wi-Fi", "en7": "Ethernet"})
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].IP != "192.168.1.30" || rows[0].Interface != "en0" ||
		rows[0].ConnectionType != "Wi-Fi" || rows[2].ConnectionType != "Ethernet (wired)" {
		t.Fatalf("wrong order/labels: %+v", rows)
	}
	if !strings.Contains(rows[0].Label(), "192.168.1.30 | Wi-Fi | en0") {
		t.Fatal(rows[0].Label())
	}
}

func TestNetworkReadFailureAndEmptyList(t *testing.T) {
	rows, err := collectNetworks(nil, nil, nil)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatal("empty list must encode as []")
	}
	_, err = collectNetworks([]net.Interface{{Name: "en0", Flags: net.FlagUp}},
		func(net.Interface) ([]net.Addr, error) { return nil, errors.New("test failure") }, nil)
	if err == nil {
		t.Fatal("missing interface error")
	}
}

func TestMetadataOutputBoundAndTerminalSanitization(t *testing.T) {
	var copied boundedOutput
	if _, err := io.Copy(&copied, io.LimitReader(strings.NewReader(strings.Repeat("x", 70000)), 70000)); err == nil {
		t.Fatal("io.Copy bypassed output bound")
	}
	var out boundedOutput
	if _, err := out.Write(make([]byte, 65536)); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("x")); err == nil || out.Len() != 65536 {
		t.Fatal("output bound not enforced")
	}
	port := cleanLabel("\x1b[31mEthernet\r\t\x00")
	if strings.ContainsAny(port, "\x1b\r\t\x00") {
		t.Fatal("unsafe terminal characters")
	}
	if len([]rune(cleanLabel(strings.Repeat("x", 200)))) != 120 {
		t.Fatal("unbounded label")
	}
	ports := parseHardwarePorts("Hardware Port: Ethernet\n\nDevice: en0\nDevice: en1\n")
	if len(ports) != 0 {
		t.Fatal("stale hardware port reused")
	}
}

func TestEndpointLoopbackLabel(t *testing.T) {
	for _, endpoint := range []string{"127.0.0.1:9443", "[::1]:9443"} {
		if !strings.Contains(EndpointConnection(endpoint), "Loopback") {
			t.Fatal("loopback mislabeled")
		}
	}
}
