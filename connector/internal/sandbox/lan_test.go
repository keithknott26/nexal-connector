package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGuestNICsAreStableDistinctAndLocal(t *testing.T) {
	if n := guestNICs("7e8f8f8e-6136-450f-b90c-4f471b29974e", false); n != (guestNIC{}) {
		t.Fatalf("no LAN must pin nothing: %+v", n)
	}
	a := guestNICs("7e8f8f8e-6136-450f-b90c-4f471b29974e", true)
	b := guestNICs("7e8f8f8e-6136-450f-b90c-4f471b29974e", true)
	c := guestNICs("e2264d25-3678-4966-9530-0594bebfc666", true)
	if a != b {
		t.Fatal("NIC addresses must be stable for a sandbox")
	}
	if a.NATMAC == a.LANMAC || a.NATMAC == c.NATMAC || a.LANMAC == c.LANMAC {
		t.Fatalf("addresses collide: %+v %+v", a, c)
	}
	for _, mac := range []string{a.NATMAC, a.LANMAC, c.NATMAC, c.LANMAC} {
		if !macPattern.MatchString(mac) {
			t.Errorf("%s is not a locally administered unicast MAC", mac)
		}
	}
	if a.Socket != LANBridgeSocket {
		t.Fatalf("socket %q", a.Socket)
	}
}

func lanSeed() SeedParams {
	p := runtimeSeed()
	n := guestNICs(p.SandboxID, true)
	p.NATMAC, p.LANMAC = n.NATMAC, n.LANMAC
	return p
}

func TestNetworkConfigNamesBothNICs(t *testing.T) {
	p := lanSeed()
	nc := RenderNetworkConfig(p)
	for _, want := range []string{
		"version: 2\n", "  nat0:\n", "      macaddress: \"" + p.NATMAC + "\"\n", "    set-name: nat0\n",
		"  lan0:\n", "      macaddress: \"" + p.LANMAC + "\"\n", "    set-name: lan0\n",
		"      route-metric: 100\n", "      route-metric: 200\n", "    optional: true\n",
	} {
		if !strings.Contains(nc, want) {
			t.Errorf("network-config lacks %q", want)
		}
	}
	// the default route stays on nat0 (the mesh and downloads), lan0 is optional
	if strings.Index(nc, "route-metric: 100") > strings.Index(nc, "lan0:") {
		t.Error("nat0 must carry the better route metric")
	}
}

func TestSeedWritesNetworkConfigOnlyWithALanNIC(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSeedDir(filepath.Join(dir, "plain"), runtimeSeed()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plain", "network-config")); !os.IsNotExist(err) {
		t.Fatal("a NAT-only VM must keep the image's default network config")
	}
	if err := WriteSeedDir(filepath.Join(dir, "lan"), lanSeed()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "lan", "network-config"))
	if err != nil || !strings.Contains(string(b), "lan0") {
		t.Fatalf("network-config: %v %q", err, b)
	}
}

func TestValidateSeedNICs(t *testing.T) {
	p := lanSeed()
	if err := ValidateSeed(p); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []func(*SeedParams){
		func(p *SeedParams) { p.NATMAC = "" },
		func(p *SeedParams) { p.LANMAC = p.NATMAC },
		func(p *SeedParams) { p.LANMAC = "03:00:00:00:00:01" },           // multicast
		func(p *SeedParams) { p.NATMAC = "02:00:00:00:00:0Z" },           // not hex
		func(p *SeedParams) { p.LANMAC = "02:00:00:00:00:01\"\n  x: y" }, // injection
	} {
		q := lanSeed()
		bad(&q)
		if ValidateSeed(q) == nil {
			t.Errorf("accepted %q / %q", q.NATMAC, q.LANMAC)
		}
	}
}

func TestFirstBootLanIP(t *testing.T) {
	fb, ok, _, _ := ParseFirstBootLine(`NEXAL-FIRSTBOOT {"meshIp":"100.86.1.2","lanIp":"192.168.68.77"}`)
	if !ok || fb.LanIP != "192.168.68.77" {
		t.Fatalf("%+v %v", fb, ok)
	}
	for _, bad := range []string{"169.254.3.4", "8.8.8.8", "fe80::1", "nope"} {
		fb, ok, _, _ := ParseFirstBootLine(`NEXAL-FIRSTBOOT {"meshIp":"100.86.1.2","lanIp":"` + bad + `"}`)
		if !ok || fb.LanIP != "" || fb.MeshIP != "100.86.1.2" {
			t.Errorf("%s: a bad LAN address must be dropped, not the report: %+v %v", bad, fb, ok)
		}
	}
}

// The seed's first-boot script reports the LAN address and the app installer
// publishes on it.
func TestScriptsUseTheLanNIC(t *testing.T) {
	if !strings.Contains(seedFirstBootScript, `extra="$extra,\"lanIp\":\"$lan\""`) {
		t.Error("first boot does not report lanIp")
	}
	for _, want := range []string{`set -- "$@" -p "$LANIP:8096:8096"`, `set -- "$@" -p "$LANIP:8123:8123"`} {
		if !strings.Contains(appSetupScript, want) {
			t.Errorf("app setup lacks %q", want)
		}
	}
}

func TestStateFileCarriesLanIP(t *testing.T) {
	b, _ := json.Marshal(record{ID: "x", LAN: true, LanIP: "192.168.68.77"})
	if !strings.Contains(string(b), `"lanIp":"192.168.68.77"`) || !strings.Contains(string(b), `"lan":true`) {
		t.Fatalf("%s", b)
	}
}

type specHyp struct {
	fakeHyp
	mu    sync.Mutex
	specs []Spec
}

func (s *specHyp) Start(ctx context.Context, sp Spec) (Handle, error) {
	s.mu.Lock()
	s.specs = append(s.specs, sp)
	s.mu.Unlock()
	return s.fakeHyp.Start(ctx, sp)
}

// A guest first booted with a LAN NIC keeps both MACs on restart; one booted
// without keeps nexal-vmhost's own NAT address (no pinning, no LAN NIC).
func TestRestartKeepsTheGuestsNICs(t *testing.T) {
	old := lanBridgeAvailable
	defer func() { lanBridgeAvailable = old }()
	for _, tc := range []struct {
		lan, helper bool
		wantSock    bool
	}{{true, true, true}, {true, false, false}, {false, true, false}} {
		lanBridgeAvailable = func() bool { return tc.helper }
		hv := &specHyp{}
		m := newTestManager(t, hv, nil, &fakeGuest{rep: GuestReply{OK: true}})
		id := "7e8f8f8e-6136-450f-b90c-4f471b29974e"
		if err := os.MkdirAll(m.boxDir(id), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(m.diskPath(id), []byte("d"), 0o600); err != nil {
			t.Fatal(err)
		}
		put(m, &record{ID: id, State: StateRunning, Kind: SandboxVM, Lifecycle: LifecyclePersistent, LAN: tc.lan,
			Boot: &BootInfo{Hostname: "h"}})
		if _, err := m.restartVM(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		sp := hv.specs[len(hv.specs)-1]
		want := guestNICs(id, tc.lan)
		if sp.MACAddress != want.NATMAC || sp.LanMACAddress != want.LANMAC || (sp.LanSocket != "") != tc.wantSock {
			t.Errorf("lan=%v helper=%v: spec %+v", tc.lan, tc.helper, sp)
		}
	}
}
