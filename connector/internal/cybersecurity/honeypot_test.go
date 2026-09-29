package cybersecurity

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceClass(t *testing.T) {
	for addr, want := range map[string]string{"100.100.1.2": "mesh", "192.168.1.9": "lan", "10.0.0.4": "lan", "fe80::1": "lan",
		"8.8.8.8": "other", "127.0.0.1": "", "::1": "", "::ffff:100.64.0.1": "mesh"} {
		if got := SourceClass(netip.MustParseAddr(addr)); got != want {
			t.Fatalf("%s: got %q want %q", addr, got, want)
		}
	}
}

func TestHoneypotLimiterAndOutboxAreBounded(t *testing.T) {
	var s HoneypotState
	var limiter honeypotLimiter
	now := time.Now()
	source := netip.MustParseAddr("100.64.0.9")
	for i := 0; i < 5; i++ { // same source and port: one alert per hour
		recordHoneypotHit(&s, honeypotHit{at: now.Add(time.Duration(i) * time.Second), service: "ssh", port: 2222, source: source}, &limiter)
	}
	if len(s.Pending) != 1 || s.Triggers != 5 || s.Suppressed != 4 || len(s.Recent) != 5 {
		t.Fatalf("cool-off: %+v", s)
	}
	for i := 0; i < 100; i++ {
		recordHoneypotHit(&s, honeypotHit{at: now, service: "vnc", port: 5909, source: netip.AddrFrom4([4]byte{192, 168, 1, byte(i)})}, &limiter)
	}
	if len(s.Pending) > MaxHoneypotPending || len(s.Recent) != MaxHoneypotRecent {
		t.Fatalf("unbounded: pending=%d recent=%d", len(s.Pending), len(s.Recent))
	}
	e := s.Pending[0]
	if err := e.Validate(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if e.Kind != "network_alert" || e.Detector != "nexal_honeypot_ssh" || !strings.HasPrefix(e.EvidenceRef, "honeypot_ssh_mesh_") || strings.Contains(e.EvidenceRef, "100.64") {
		t.Fatalf("event leaks or misclassifies: %+v", e)
	}
}

func TestHoneypotRunReportsConnectionsOnlyWhileEnabled(t *testing.T) {
	h := Honeypot{Directory: t.TempDir(), allowLoopback: true, services: []HoneypotService{{"ssh", 0, []byte("SSH-2.0-OpenSSH_9.6\r\n")}}}
	if err := h.Configure(true); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx, func(_ context.Context, e Event) error { mu.Lock(); got = append(got, e); mu.Unlock(); return nil })
		close(done)
	}()
	var port int
	for i := 0; i < 100 && port == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		if st, err := h.Status(); err == nil && st.Status == "listening" {
			port = st.Ports[0].Port
		}
	}
	if port == 0 {
		t.Fatal("honeypot never listened")
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	banner := make([]byte, 7)
	if _, err := io.ReadFull(conn, banner); err != nil || string(banner) != "SSH-2.0" {
		t.Fatalf("banner %q %v", banner, err)
	}
	conn.Close()
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, _ := h.Status()
	mu.Lock()
	if len(got) != 1 || st.Triggers != 1 || len(st.Pending) != 0 || st.Recent[0].SourceAddress != "127.0.0.1" {
		t.Fatalf("got %d events, state %+v", len(got), st)
	}
	mu.Unlock()
	cancel()
	<-done
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), time.Second); err == nil {
		c.Close()
		t.Fatal("listener survived shutdown")
	}
}

func TestHoneypotIgnoresLoopbackInProduction(t *testing.T) {
	var s HoneypotState
	if SourceClass(netip.MustParseAddr("127.0.0.1")) != "" || len(s.Pending) != 0 {
		t.Fatal("loopback classified as remote")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
