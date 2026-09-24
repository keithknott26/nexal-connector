package mesh

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const sample = `{"peers":{"total":1,"connected":0,"details":[{"fqdn":"keiths-macbook-pro.netbird.cloud","publicKey":"k1","status":"Idle","connectionType":"-","lastWireguardHandshake":"0001-01-01T00:00:00Z","transferReceived":0,"transferSent":0,"quantumResistance":false,"latency":0}]},"management":{"url":"https://api.netbird.io:443","connected":true},"quantumResistance":true}`

func TestRuntimeConnectedToNetworkWithIdlePeer(t *testing.T) {
	s := translateRuntime([]byte(sample), time.Now())
	if !s.ProviderAvailable || s.Lifecycle != LifecycleConnected {
		t.Fatalf("lifecycle %v", s.Lifecycle)
	}
	if len(s.Peers) != 1 || s.Peers[0].Lifecycle != LifecycleUnavailable || s.PQ != PQDegraded {
		t.Fatalf("%+v", s)
	}
	if strings.Contains(s.Peers[0].Name, "netbird") {
		t.Fatal("upstream domain leaked")
	}
}

func TestRuntimeProtectedPeer(t *testing.T) {
	js := `{"peers":{"details":[{"fqdn":"m2.x","netbirdIp":"100.113.174.101/16","publicKey":"k","status":"Connected","connectionType":"P2P","quantumResistance":true,"latency":12000000,"transferReceived":5,"transferSent":7}]},"management":{"connected":true},"quantumResistance":true}`
	s := translateRuntime([]byte(js), time.Now())
	if s.PQ != PQProtected || s.Peers[0].Path != PathDirect || s.Peers[0].LatencyMS != 12 || s.Peers[0].TunnelAddress != "100.113.174.101" || !s.StrictPQReady() {
		t.Fatalf("%+v", s)
	}
}

func TestRuntimeFailureIsUnavailable(t *testing.T) {
	p := RuntimeProvider{Run: func(context.Context) ([]byte, error) { return nil, errors.New("daemon down") }}
	if s := p.Snapshot(); s.Lifecycle != LifecycleUnavailable {
		t.Fatalf("%v", s.Lifecycle)
	}
	if s := translateRuntime([]byte("not json"), time.Now()); s.Lifecycle != LifecycleUnavailable {
		t.Fatal("bad json should be unavailable")
	}
	if s := translateRuntime([]byte(`{"management":{"connected":false}}`), time.Now()); s.Lifecycle != LifecycleAuthenticating {
		t.Fatal("disconnected management should be authenticating")
	}
}
