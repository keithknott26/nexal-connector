package mesh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeProviderMapsP2PRelayAndQuantumEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nexal-network")
	body := `#!/bin/sh
printf '%s' '{"daemonStatus":"Connected","quantumResistance":true,"management":{"connected":true},"signal":{"connected":true},"peers":{"details":[{"fqdn":"m2.private.invalid","netbirdIp":"100.64.0.2","status":"Connected","connectionType":"P2P","transferReceived":12,"transferSent":34,"latency":2500000,"quantumResistance":true},{"fqdn":"m4.private.invalid","netbirdIp":"100.64.0.4","status":"Connected","connectionType":"Relayed","transferReceived":56,"transferSent":78,"quantumResistance":true}]}}'
`
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { t.Fatal(err) }
	got := (RuntimeProvider{Executable: path, Timeout: time.Second}).Snapshot()
	if !got.ProviderAvailable || got.Lifecycle != LifecycleConnected || got.PQ != PQProtected { t.Fatalf("status = %+v", got) }
	if len(got.Peers) != 2 || got.Peers[0].Name != "m2" || got.Peers[0].Path != PathDirect || got.Peers[1].Path != PathRelay { t.Fatalf("peers = %+v", got.Peers) }
	if got.Peers[1].PathLabel != "neXal Relay — metered" { t.Fatalf("relay label = %q", got.Peers[1].PathLabel) }
}

func TestRuntimeProviderFailsClosedOnUnreadableStatus(t *testing.T) {
	got := (RuntimeProvider{Executable: "/usr/bin/false", Timeout: time.Second}).Snapshot()
	if got.ProviderAvailable || got.Lifecycle != LifecycleUnavailable || got.PQ != PQUnsupported { t.Fatalf("status = %+v", got) }
}
