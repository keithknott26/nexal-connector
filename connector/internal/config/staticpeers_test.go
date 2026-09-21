package config

import (
	"encoding/json"
	"strings"
	"testing"

	"nexal/connector/internal/pool"
)

const (
	fpA = "aa11223344556677889900112233445566778899001122334455667788990011"
	fpB = "bb11223344556677889900112233445566778899001122334455667788990011"
)

// Endpoint cases shared by the config rule and the pool rule. The cross-VLAN
// case (a routed RFC1918 address on another subnet) is in the ACCEPT list on
// purpose: the transport already permitted it, which is why this change adds no
// relaxation.
var endpointCases = []struct {
	endpoint string
	accept   bool
	why      string
}{
	{"https://10.20.0.5:8443", true, "cross-VLAN RFC1918 address: already permitted, no relaxation needed"},
	{"https://192.168.1.20:8443", true, "same-subnet RFC1918"},
	{"https://172.16.4.4:443", true, "RFC1918 172.16/12"},
	{"https://127.0.0.1:8443", true, "loopback"},
	{"https://[fd00::1]:8443", true, "IPv6 unique local is private"},
	{"https://[fe80::1]:8443", true, "IPv6 link-local unicast"},
	{"https://169.254.10.1:8443", true, "IPv4 link-local unicast"},
	{"https://203.0.113.9:8443", false, "PUBLIC address: refused, and this is the rule the doc says must not be relaxed"},
	{"https://100.64.0.1:8443", false, "CGNAT is not private to net.IP.IsPrivate"},
	{"https://8.8.8.8:443", false, "public"},
	{"https://peer.example:8443", false, "hostname: the peer transport performs no DNS lookup"},
	{"http://10.20.0.5:8443", false, "plaintext"},
	{"https://10.20.0.5", false, "no explicit port"},
	{"https://10.20.0.5:0", false, "port zero"},
	{"https://user:pw@10.20.0.5:8443", false, "credentials in URL"},
	{"https://10.20.0.5:8443/objects", false, "path"},
	{"https://10.20.0.5:8443?x=1", false, "query"},
	{"https://10.20.0.5:8443#f", false, "fragment"},
	{"https://0.0.0.0:8443", false, "unspecified"},
	{"https://224.0.0.251:8443", false, "multicast"},
	{"", false, "empty"},
}

// THE DRIFT GUARD. config re-states pool's endpoint rule because importing pool
// would be an import cycle (pool's tests import discovery → client → config).
// This test imports pool from the TEST binary, which is legal, and asserts the
// two functions agree on every case above. If either side is loosened, this
// fails.
func TestStaticPeerEndpointRuleMatchesPool(t *testing.T) {
	for _, c := range endpointCases {
		t.Run(c.endpoint, func(t *testing.T) {
			got := ValidPeerEndpoint(c.endpoint)
			poolAccepts := pool.ValidPeerEndpoint(c.endpoint) == nil
			if got != poolAccepts {
				t.Fatalf("config accepts=%v but pool accepts=%v (%s)", got, poolAccepts, c.why)
			}
			if got != c.accept {
				t.Fatalf("accepted=%v, want %v (%s)", got, c.accept, c.why)
			}
		})
	}
}

func TestStaticPeerValidation(t *testing.T) {
	good := StaticPeer{Endpoint: "https://10.20.0.5:8443", Fingerprint: fpA, Label: "studio vlan 20"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	// A static address must NOT be a way to skip the fingerprint pin.
	if err := (StaticPeer{Endpoint: "https://10.20.0.5:8443"}).Validate(); err == nil {
		t.Fatal("an endpoint without a fingerprint was accepted; the allowlist is an exact fingerprint allowlist")
	}
	if err := (StaticPeer{Endpoint: "https://10.20.0.5:8443", Fingerprint: strings.ToUpper(fpA)}).Validate(); err == nil {
		t.Fatal("uppercase fingerprint accepted; it must be byte-identical to pool.DeviceID output")
	}
	if err := (StaticPeer{Endpoint: "https://203.0.113.9:8443", Fingerprint: fpA}).Validate(); err == nil {
		t.Fatal("public endpoint accepted")
	}
	if err := (StaticPeer{Endpoint: "https://10.20.0.5:8443", Fingerprint: fpA, Label: "a\nb"}).Validate(); err == nil {
		t.Fatal("control character in label accepted")
	}
}

func TestStaticPeerAddrPort(t *testing.T) {
	addr, ok := StaticPeer{Endpoint: "https://10.20.0.5:8443", Fingerprint: fpA}.AddrPort()
	if !ok || addr.String() != "10.20.0.5:8443" {
		t.Fatalf("addr = %v ok=%v", addr, ok)
	}
	if _, ok := (StaticPeer{Endpoint: "nonsense"}).AddrPort(); ok {
		t.Fatal("garbage endpoint produced an address")
	}
	if addr, ok := (StaticPeer{Endpoint: "https://[fd00::1]:8443"}).AddrPort(); !ok || addr.Port() != 8443 {
		t.Fatalf("ipv6 addr = %v ok=%v", addr, ok)
	}
}

func TestDecodeStaticPeerStrict(t *testing.T) {
	good := `{"endpoint":"https://10.20.0.5:8443","fingerprint":"` + fpA + `"}`
	p, err := DecodeStaticPeer([]byte(good))
	if err != nil || p.Endpoint != "https://10.20.0.5:8443" || p.Fingerprint != fpA {
		t.Fatalf("p = %+v err = %v", p, err)
	}
	// Optional label decodes; a payload without it (above) still decodes, which
	// is the backward-compatibility property.
	withLabel := `{"endpoint":"https://10.20.0.5:8443","fingerprint":"` + fpA + `","label":"mini"}`
	if p, err := DecodeStaticPeer([]byte(withLabel)); err != nil || p.Label != "mini" {
		t.Fatalf("p = %+v err = %v", p, err)
	}
	for name, body := range map[string]string{
		"missing endpoint":    `{"fingerprint":"` + fpA + `"}`,
		"missing fingerprint": `{"endpoint":"https://10.20.0.5:8443"}`,
		"null endpoint":       `{"endpoint":null,"fingerprint":"` + fpA + `"}`,
		"empty endpoint":      `{"endpoint":"","fingerprint":"` + fpA + `"}`,
		"duplicate key":       `{"endpoint":"https://10.20.0.5:8443","endpoint":"https://10.20.0.6:8443","fingerprint":"` + fpA + `"}`,
		"unknown field":       `{"endpoint":"https://10.20.0.5:8443","fingerprint":"` + fpA + `","trusted":true}`,
		"trailing json":       `{"endpoint":"https://10.20.0.5:8443","fingerprint":"` + fpA + `"} {}`,
		"not an object":       `["https://10.20.0.5:8443"]`,
		"public endpoint":     `{"endpoint":"https://203.0.113.9:8443","fingerprint":"` + fpA + `"}`,
		"numeric endpoint":    `{"endpoint":5,"fingerprint":"` + fpA + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStaticPeer([]byte(body)); err == nil {
				t.Fatalf("accepted %s", body)
			}
		})
	}
}

func TestAddAndRemoveStaticPeers(t *testing.T) {
	a := StaticPeer{Endpoint: "https://10.20.0.5:8443", Fingerprint: fpA}
	b := StaticPeer{Endpoint: "https://10.30.0.5:8443", Fingerprint: fpB}
	list, err := AddStaticPeer(nil, b)
	if err != nil {
		t.Fatal(err)
	}
	list, err = AddStaticPeer(list, a)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by fingerprint, so the stored file is stable across edits.
	if len(list) != 2 || list[0].Fingerprint != fpA {
		t.Fatalf("list = %+v", list)
	}
	if _, err := AddStaticPeer(list, a); err == nil {
		t.Fatal("duplicate fingerprint accepted; it must be removed explicitly first")
	}
	if _, err := AddStaticPeer(list, StaticPeer{Endpoint: a.Endpoint, Fingerprint: "cc" + fpA[2:]}); err == nil {
		t.Fatal("duplicate endpoint accepted")
	}
	shorter, err := RemoveStaticPeer(list, fpA)
	if err != nil || len(shorter) != 1 || shorter[0].Fingerprint != fpB {
		t.Fatalf("shorter = %+v err = %v", shorter, err)
	}
	if _, err := RemoveStaticPeer(list, "dd"+fpA[2:]); err == nil {
		t.Fatal("removing an absent fingerprint reported success")
	}
	// Bound enforced.
	many := make([]StaticPeer, MaxStaticPeers+1)
	for i := range many {
		many[i] = StaticPeer{Endpoint: "https://10.20.0.5:8443", Fingerprint: fpA}
	}
	if err := ValidateStaticPeers(many); err == nil {
		t.Fatal("unbounded static peer list accepted")
	}
}

// A config written before staticPeers existed must still load, and a loaded
// config with a bad entry must be refused rather than silently dropped.
func TestConfigWithAndWithoutStaticPeers(t *testing.T) {
	base := Config{Version: 1, Coordinator: "https://coordinator.example", Name: "mac",
		Listen: "127.0.0.1:8788", Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
	if err := base.Validate(); err != nil {
		t.Fatalf("a config with no staticPeers must still validate: %v", err)
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "staticPeers") {
		t.Fatal("an empty static peer list must not be written into the file (omitempty)")
	}
	ok := base
	ok.StaticPeers = []StaticPeer{{Endpoint: "https://10.20.0.5:8443", Fingerprint: fpA}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.StaticPeers = []StaticPeer{{Endpoint: "https://203.0.113.9:8443", Fingerprint: fpA}}
	if err := bad.Validate(); err == nil {
		t.Fatal("a config naming a public peer endpoint validated")
	}
}
