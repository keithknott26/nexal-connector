package mesh

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

// captureDiag installs a debug-level logger writing to a buffer and restores
// the global diagnostic state when the test ends.
func captureDiag(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	SetDiagnosticLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { SetDiagnosticLogger(nil) })
	return &buf
}

func TestNoteRuntimeFailureLogsFirstAtInfoThenDebug(t *testing.T) {
	buf := captureDiag(t)
	err := errors.New("boom")
	noteRuntimeFailure(err)
	noteRuntimeFailure(err)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "level=INFO") || strings.Contains(lines[0], "repeat") {
		t.Fatalf("first failure should be Info without repeat: %s", lines[0])
	}
	if !strings.Contains(lines[1], "level=DEBUG") || !strings.Contains(lines[1], "repeat=true") {
		t.Fatalf("repeat failure should be Debug repeat=true: %s", lines[1])
	}
}

func TestNoteRuntimeStatusLogsChangesOnly(t *testing.T) {
	buf := captureDiag(t)
	now := time.Now()
	first := []byte(`{"management":{"connected":true},"netbirdIp":"100.64.0.1/16","daemonVersion":"1.0",
		"peers":{"details":[{"fqdn":"a.example","status":"Connected","connectionType":"p2p"}]}}`)

	noteRuntimeFailure(errors.New("boom"))
	buf.Reset()
	noteRuntimeStatus(first, now)
	got := buf.String()
	for _, want := range []string{"readable again", "mesh runtime version", "mesh service state", "mesh routes", "mesh peer added", "mesh peer pq state"} {
		if !strings.Contains(got, want) {
			t.Errorf("first poll missing %q in:\n%s", want, got)
		}
	}

	buf.Reset()
	noteRuntimeStatus(first, now)
	if buf.Len() != 0 {
		t.Fatalf("identical poll must log nothing, got:\n%s", buf.String())
	}

	buf.Reset()
	noteRuntimeStatus([]byte(`{"management":{"connected":true},"netbirdIp":"100.64.0.1/16","daemonVersion":"1.0","peers":{"details":[]}}`), now)
	if !strings.Contains(buf.String(), "mesh peer removed") || !strings.Contains(buf.String(), "a.example") {
		t.Fatalf("removed peer not logged:\n%s", buf.String())
	}

	buf.Reset()
	noteRuntimeStatus([]byte("not json"), now)
	if !strings.Contains(buf.String(), "mesh runtime status unreadable") {
		t.Fatalf("unreadable status not logged:\n%s", buf.String())
	}
}

func TestNoteRuntimeWithoutLoggerIsNoop(t *testing.T) {
	SetDiagnosticLogger(nil)
	noteRuntimeFailure(errors.New("boom"))
	noteRuntimeStatus([]byte(`{}`), time.Now())
	if diag.last != nil || diag.lastFailure != "" {
		t.Fatalf("no logger must leave state untouched: %+v %q", diag.last, diag.lastFailure)
	}
}

func TestNoteServiceProbeLogsOnlyChanges(t *testing.T) {
	buf := captureDiag(t)
	noteServiceProbe("127.0.0.1", []string{"ssh", "smb"})
	noteServiceProbe("127.0.0.1", []string{"ssh", "smb"})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("an unchanged probe must not log again: %q", buf.String())
	}
	for _, want := range []string{"target=local", "ssh=true", "vnc=false", "smb=true"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("log line missing %q: %s", want, lines[0])
		}
	}
	noteServiceProbe("127.0.0.1", []string{"vnc"})
	noteServiceProbe("100.64.0.2", []string{"vnc"})
	if got := strings.Count(buf.String(), "service probe"); got != 3 {
		t.Fatalf("a changed result or new address must log: got %d lines: %s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "target=100.64.0.2") {
		t.Fatalf("remote address should be logged as-is: %s", buf.String())
	}
}

func TestNoteServiceProbeWithoutLoggerIsNoop(t *testing.T) {
	SetDiagnosticLogger(nil)
	noteServiceProbe("127.0.0.1", []string{"ssh"})
	if len(diag.services) != 0 {
		t.Fatalf("no logger must leave state untouched: %v", diag.services)
	}
}
