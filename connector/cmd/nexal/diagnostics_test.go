package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/diaglog"
	"nexal/connector/internal/mesh"
	"nexal/connector/internal/sysinfo"
	"nexal/connector/internal/wol"
)

func TestDiagnosticsToggle(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	if err := diagnosticsCommand(ctx, []string{"--on", "--config", cfg}); err != nil {
		t.Fatal(err)
	}
	if !diaglog.Enabled(cfg) {
		t.Fatal("--on did not create the flag")
	}
	if err := diagnosticsCommand(ctx, []string{"--status", "--config", cfg}); err != nil || !diaglog.Enabled(cfg) {
		t.Fatalf("--status changed or failed: %v", err)
	}
	if err := diagnosticsCommand(ctx, []string{"--off", "--config", cfg}); err != nil {
		t.Fatal(err)
	}
	if diaglog.Enabled(cfg) {
		t.Fatal("--off did not remove the flag")
	}
	if err := diagnosticsCommand(ctx, []string{"--on", "--off", "--config", cfg}); err == nil {
		t.Fatal("--on with --off must be refused")
	}
	if err := diagnosticsCommand(ctx, []string{"--bundle", "relative.txt", "--config", cfg}); err == nil {
		t.Fatal("a relative bundle path must be refused")
	}
}

func TestDiagnosticsBundleRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{
		"bearer":   "eyJhbGciOiJFUzI1NiJ9Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5",
		"setupKey": "A1B2C3D4-E5F6-4711-9ABC-DEF012345678",
		"password": "correct-horse-9",
		"enroll":   "enr_7h3s3cr3tc0d3",
		"mac":      "a4:83:e7:12:34:56",
		"prefix":   "192.168.77.0/24",
		"lanKey":   strings.Repeat("9f", 32),
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(logs, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(diaglog.AgentLogName, `{"level":"INFO","msg":"agent started"}`+"\n"+
		`{"level":"DEBUG","msg":"oops","Authorization":"Bearer `+secrets["bearer"]+`"}`+"\n")
	write(diaglog.FileName, `{"level":"DEBUG","msg":"rejoin","setupKey":"`+secrets["setupKey"]+`","enroll":"`+secrets["enroll"]+`"}`+"\n")
	write(diaglog.AppLogName, `2026-10-02T10:00:00Z ui service link opened vnc://keith:`+secrets["password"]+`@m4.nexal`+"\n")

	src := bundleSources{
		Now: func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) },
		Status: func(context.Context) ([]byte, error) {
			return []byte(`{"version":"1.2.3","mesh":{"providerAvailable":true,"lifecycle":"connected","pq":"protected",` +
				`"peers":[{"id":"x","name":"m2","lifecycle":"connected","path":"direct","pq":"protected","tunnelAddress":"100.92.1.2"}]},` +
				`"presence":{"connected":true,"online":["h1","h2"],"hosts":[]},` +
				`"wake":{"macs":["` + secrets["mac"] + `"],"wakeForNetwork":"enabled","reported":true}}`), nil
		},
		Runtime: func(context.Context) (mesh.RuntimeDetail, error) {
			return mesh.RuntimeDetail{DaemonVersion: "0.59.0", CLIVersion: "0.59.0", ManagementConnected: true,
				QuantumResistance: true, SelfAddress: "100.92.1.1", Networks: []string{},
				Peers: []mesh.RuntimePeerDetail{{FQDN: "m2.nexal.local", TunnelAddress: "100.92.1.2", ConnStatus: "connected",
					ConnectionType: "p2p", ICELocal: "host", ICERemote: "host", PQ: mesh.PQProtected}}}, nil
		},
		Wake: func(context.Context) wol.Facts {
			return wol.Facts{MACs: []string{secrets["mac"]}, Prefixes: []string{secrets["prefix"]}, LANKey: secrets["lanKey"],
				WakeForNetwork: wol.WakeEnabled}
		},
		Host:   func(context.Context) sysinfo.Info { return sysinfo.Info{OS: "macOS 27.0", Model: "Mac mini", Chip: "Apple M4"} },
		LogDir: logs,
	}
	out := buildDiagnosticsBundle(context.Background(), cfg, src)
	for name, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("%s %q leaked into the bundle:\n%s", name, s, out)
		}
	}
	for _, want := range []string{
		"runtimeVersion: daemon=0.59.0", "macs=1", "lanKeyPrefixes=1", "lanKeyPresent=true",
		"lastReportAccepted=true", "presence: connected=true online=2", "pq=protected",
		"tunnelAddress: 100.92.1.1", "agent started", "m4.nexal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bundle is missing %q:\n%s", want, out)
		}
	}
}
