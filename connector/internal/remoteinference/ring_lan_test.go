package remoteinference

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPlanHostsPrefersSharedLAN(t *testing.T) {
	_, home, _ := net.ParseCIDR("192.168.1.0/24")
	ring := []string{"100.86.0.1:8010", "100.86.0.2:8011", "100.86.0.3:8012"}
	lan := []string{"192.168.1.10:8010", "192.168.1.20:8011", "10.0.0.5:8012"} // the third Mac is on another LAN
	hosts, mesh, lanPeers := planHosts(ring, lan, 1, 8011, []*net.IPNet{home})
	want := []string{"192.168.1.10:8010", "127.0.0.1:8011", "100.86.0.3:8012"}
	if strings.Join(hosts, ",") != strings.Join(want, ",") {
		t.Fatalf("hosts %v, want %v", hosts, want)
	}
	if !mesh["100.86.0.1"] || !mesh["100.86.0.3"] || mesh["100.86.0.2"] || !lanPeers["192.168.1.10"] || len(lanPeers) != 1 {
		t.Fatalf("relay allow lists: mesh %v lan %v", mesh, lanPeers)
	}
	hosts, _, lanPeers = planHosts(ring, nil, 0, 8010, nil) // no LAN addresses published: neXal only
	if hosts[1] != ring[1] || hosts[0] != "127.0.0.1:8010" || len(lanPeers) != 0 {
		t.Fatalf("mesh-only plan %v", hosts)
	}
}

func TestRingRelayAcceptsOnlyThePool(t *testing.T) {
	target, _ := net.Listen("tcp", "127.0.0.1:0")
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	listen := freeAddr(t)
	try := func(allow map[string]bool) string {
		rl, err := ringRelay(context.Background(), listen, target.Addr().String(), allow)
		if err != nil {
			t.Fatal(err)
		}
		defer rl.Close()
		c, err := net.Dial("tcp", listen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = c.Write([]byte("ring"))
		b := make([]byte, 4)
		n, _ := io.ReadFull(c, b)
		return string(b[:n])
	}
	if got := try(map[string]bool{"127.0.0.1": true}); got != "ring" {
		t.Fatalf("allowed peer got %q", got)
	}
	if got := try(map[string]bool{"100.86.0.9": true}); got != "" {
		t.Fatalf("stranger got %q", got)
	}
}

func TestRingLANAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	taken := l.Addr().(*net.TCPAddr).Port
	defer l.Close()
	f, _ := net.Listen("tcp", "127.0.0.1:0")
	free := f.Addr().(*net.TCPAddr).Port
	f.Close()

	r := &Runner{LAN: func() []string { return []string{"not-an-ip", "127.0.0.1"} }}
	if got, want := r.ringLAN("100.86.0.1", free), net.JoinHostPort("127.0.0.1", strconv.Itoa(free)); got != want {
		t.Fatalf("free port: got %q, want %q", got, want)
	}
	if got := r.ringLAN("100.86.0.1", taken); got != "" {
		t.Fatalf("port in use on the LAN: got %q, want none", got)
	}
	if got := r.ringLAN("127.0.0.1", free); got != "" {
		t.Fatalf("loopback neXal address: got %q, want none", got)
	}
	if got := r.ringLAN("garbage", free); got != "" {
		t.Fatalf("unparseable neXal address: got %q, want none", got)
	}
	r.NoLAN = true
	if got := r.ringLAN("100.86.0.1", free); got != "" {
		t.Fatalf("NoLAN: got %q, want none", got)
	}
}

func TestPlanRingKeepsCoordinatorRingWithoutLAN(t *testing.T) {
	r := &Runner{}
	ring := []string{"100.86.0.1:8010", "100.86.0.2:8011"}
	for _, tc := range []struct {
		name string
		addr string
		rank int
	}{
		{"loopback address", "127.0.0.1", 0},
		{"unparseable address", "nope", 1},
		{"rank outside ring", "100.86.0.1", 5},
	} {
		plan, err := r.planRing(context.Background(), ring, []string{"192.168.1.10:8010"}, tc.rank, 8010, tc.addr)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if strings.Join(plan.hosts, ",") != strings.Join(ring, ",") || len(plan.relays) != 0 {
			t.Fatalf("%s: hosts %v relays %d", tc.name, plan.hosts, len(plan.relays))
		}
		plan.hosts[0] = "changed"
		if ring[0] == "changed" {
			t.Fatalf("%s: plan aliases the coordinator ring", tc.name)
		}
		plan.close()
	}
}

func TestOverLAN(t *testing.T) {
	cases := []struct {
		hosts []string
		want  string
	}{
		{[]string{"127.0.0.1:8010", "100.86.0.2:8011"}, ""},
		{[]string{"127.0.0.1:8010", "192.168.1.20:8011"}, " over the LAN"},
		{[]string{"10.0.0.5:8010", "127.0.0.1:8011"}, " over the LAN"},
		{[]string{"not-an-address", "127.0.0.1:8011"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := overLAN(&ringPlan{hosts: c.hosts}); got != c.want {
			t.Errorf("overLAN(%v) = %q, want %q", c.hosts, got, c.want)
		}
	}
}

func TestTailText(t *testing.T) {
	if got := tailText("short", 10); got != "short" {
		t.Errorf("under the limit: %q", got)
	}
	if got := tailText("abcdefghij", 4); got != "...ghij" {
		t.Errorf("long text: %q", got)
	}
	if got := tailText("ααββγγ", 2); got != "...γγ" { // cuts on runes, not bytes
		t.Errorf("multibyte text: %q", got)
	}
}

// lanIPNets must only ever report private, non-loopback IPv4 networks, whatever interfaces the host has.
func TestLanIPNetsOnlyPrivateIPv4(t *testing.T) {
	for _, n := range lanIPNets() {
		if n.IP.To4() == nil {
			t.Errorf("%v is not IPv4", n)
		}
		if !n.IP.IsPrivate() || n.IP.IsLoopback() {
			t.Errorf("%v is not a private, non-loopback network", n)
		}
		if n.Mask == nil {
			t.Errorf("%v has no mask", n)
		}
	}
}
