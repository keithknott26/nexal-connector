package mesh

import (
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	// No test may dial a real address.
	dialService = func(string) bool { return false }
}

func TestProbeServicesReportsOpenPortsAndCaches(t *testing.T) {
	var calls atomic.Int32
	prev := dialService
	defer func() { dialService = prev }()
	dialService = func(addr string) bool {
		calls.Add(1)
		return addr == "100.64.0.9:22" || addr == "100.64.0.9:445"
	}
	got := probeServices("100.64.0.9")
	if len(got) != 2 || got[0] != "ssh" || got[1] != "smb" {
		t.Fatalf("services = %v", got)
	}
	probeServices("100.64.0.9")
	if int(calls.Load()) != len(servicePorts) {
		t.Fatalf("expected cached second probe, dialed %d times", calls.Load())
	}
}

func TestRuntimeReportsDirectEndpoint(t *testing.T) {
	js := `{"peers":{"details":[{"fqdn":"m2.x","netbirdIp":"100.113.99.69","publicKey":"k","status":"Connected","connectionType":"P2P","iceCandidateType":{"local":"host","remote":"host"},"iceCandidateEndpoint":{"local":"192.168.68.51:51820","remote":"192.168.68.56:51820"}}]},"management":{"connected":true}}`
	p := translateRuntime([]byte(js), time.Now()).Peers[0]
	if p.DirectAddress != "192.168.68.56" || !p.DirectIsPrivate {
		t.Fatalf("direct = %q private=%v", p.DirectAddress, p.DirectIsPrivate)
	}
	js = `{"peers":{"details":[{"fqdn":"m2.x","publicKey":"k","status":"Connected","connectionType":"P2P","iceCandidateEndpoint":{"remote":"73.12.34.56:51820"}}]},"management":{"connected":true}}`
	p = translateRuntime([]byte(js), time.Now()).Peers[0]
	if p.DirectAddress != "73.12.34.56" || p.DirectIsPrivate {
		t.Fatalf("direct = %q private=%v", p.DirectAddress, p.DirectIsPrivate)
	}
}
