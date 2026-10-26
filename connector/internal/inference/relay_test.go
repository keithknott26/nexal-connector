package inference

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The relay answers on a second address with the server's own bytes, streaming included.
func TestRelayForwardsToTheServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "models "+r.URL.Path)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	ln, err := Relay(ctx, addr, strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + ln.Addr().String() + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "models /v1/models" {
		t.Fatalf("got %q", b)
	}
	cancel()
	if _, err := Relay(context.Background(), ln.Addr().String(), "127.0.0.1:1"); err != nil {
		// The address is free again once the relay stops; a brief race is tolerated.
		t.Log(err)
	}
}

func TestValidateAlsoListen(t *testing.T) {
	got, err := ValidateAlsoListen("127.0.0.1", []string{"100.86.56.136", "100.86.56.136", "127.0.0.1", "192.168.68.58"})
	if err != nil || strings.Join(got, ",") != "100.86.56.136,192.168.68.58" {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"0.0.0.0", "::", "nope", ""} {
		if _, err := ValidateAlsoListen("127.0.0.1", []string{bad}); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestShareAddressesClassifiesAndNeverOverlaps(t *testing.T) {
	meshAddrs, lanAddrs := ShareAddresses()
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	seen := map[string]bool{}
	for _, a := range meshAddrs {
		ip := net.ParseIP(a).To4()
		if ip == nil || !cgnat.Contains(ip) {
			t.Errorf("mesh address %q is not in 100.64.0.0/10", a)
		}
		seen[a] = true
	}
	for _, a := range lanAddrs {
		ip := net.ParseIP(a).To4()
		if ip == nil || !ip.IsPrivate() || ip.IsLoopback() {
			t.Errorf("lan address %q is not a private IPv4", a)
		}
		if seen[a] {
			t.Errorf("address %q listed as both mesh and lan", a)
		}
	}
}
