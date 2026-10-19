package mesh

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRuntimeFailureReason(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 3").Run()
	cases := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "runtime status timed out"},
		{exitErr, "runtime status exited 3"},
		{exec.ErrNotFound, "runtime not installed"},
		{errors.New("boom"), "runtime status failed: boom"},
	}
	for _, c := range cases {
		if got := runtimeFailureReason(c.err); got != c.want {
			t.Errorf("runtimeFailureReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestParseRuntimeDetailRejectsNonJSON(t *testing.T) {
	if _, err := parseRuntimeDetail([]byte("not json"), time.Now()); err == nil {
		t.Fatal("expected error for non-JSON status")
	}
}

func TestParseRuntimeDetailNormalizesPeers(t *testing.T) {
	out := []byte(`{
		"management": {"connected": true},
		"netbirdIp": "100.64.0.1/16",
		"daemonVersion": "1.2.3",
		"networks": ["b", "a"],
		"peers": {"details": [
			{"fqdn": "z.example", "netbirdIp": "100.64.0.9/16", "status": "Connected", "connectionType": "P2P", "latency": 12345678, "lastWireguardHandshake": "0001-01-01T00:00:00Z"},
			{"fqdn": "a.example", "status": "Idle"}
		]}
	}`)
	d, err := parseRuntimeDetail(out, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.SelfAddress != "100.64.0.1" || !d.ManagementConnected || d.DaemonVersion != "1.2.3" {
		t.Fatalf("unexpected detail: %+v", d)
	}
	if strings.Join(d.Networks, ",") != "a,b" {
		t.Fatalf("networks not sorted: %v", d.Networks)
	}
	if len(d.Peers) != 2 || d.Peers[0].FQDN != "a.example" || d.Peers[1].FQDN != "z.example" {
		t.Fatalf("peers not sorted: %+v", d.Peers)
	}
	z := d.Peers[1]
	if z.TunnelAddress != "100.64.0.9" || z.ConnStatus != "connected" || z.ConnectionType != "p2p" {
		t.Fatalf("peer not normalized: %+v", z)
	}
	if z.LatencyMS != 12.3 {
		t.Fatalf("latency = %v, want 12.3", z.LatencyMS)
	}
	if z.LastHandshake != "" {
		t.Fatalf("zero handshake should be dropped, got %q", z.LastHandshake)
	}
	if z.PQ != PQDegraded || z.PQReason == "" {
		t.Fatalf("peer without quantum evidence must be degraded with a reason: %+v", z)
	}
}

func TestParseRuntimeDetailEmptyNetworksNotNil(t *testing.T) {
	d, err := parseRuntimeDetail([]byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.Networks == nil || d.Peers == nil {
		t.Fatalf("Networks and Peers must be non-nil for JSON: %+v", d)
	}
}

func TestRouteOutputEnforcesLimit(t *testing.T) {
	var b routeOutput
	if _, err := b.Write(make([]byte, maxExitStatusBytes)); err != nil {
		t.Fatal(err)
	}
	if n, err := b.Write([]byte("x")); err == nil || n != 0 {
		t.Fatalf("write past limit = (%d, %v), want refusal", n, err)
	}
	if b.Len() != maxExitStatusBytes {
		t.Fatalf("buffer grew past limit: %d", b.Len())
	}
}
