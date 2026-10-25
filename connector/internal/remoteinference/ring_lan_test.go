package remoteinference

import (
	"context"
	"io"
	"net"
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
