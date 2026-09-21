package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/agent"
	"nexal/connector/internal/config"
	"nexal/connector/internal/stun"
)

func validConfig() config.Config {
	return config.Config{Version: 1, Coordinator: "https://private-coordinator.test",
		Name: "private-owner-machine", HostID: "private-host-id", Listen: "127.0.0.1:8788", Paused: true,
		MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
}
func status(t *testing.T, report Report, id string) string {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	t.Fatalf("missing check %s", id)
	return ""
}
func deps(c config.Config, telemetry agent.Telemetry) dependencies {
	return dependencies{
		load:  func(string) (config.Config, error) { return c, nil },
		probe: func(context.Context) agent.Telemetry { return telemetry },
		os:    "darwin", arch: "arm64",
	}
}
func TestDefaultDoctorDoesNotProbeOrRevealPrivateValues(t *testing.T) {
	c := validConfig()
	c.Tunnel = &config.Tunnel{TokenFile: "/private-token-path", Binary: "/private-binary", Hostname: "private-hostname"}
	d := deps(c, agent.Telemetry{})
	d.probe = func(context.Context) agent.Telemetry { t.Fatal("unexpected probe"); return agent.Telemetry{} }
	report := inspect(context.Background(), "/private-config-path", false, false, d)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{c.Name, c.Coordinator, c.HostID, c.Listen, c.Tunnel.TokenFile, c.Tunnel.Binary, c.Tunnel.Hostname, "/private-config-path"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private value disclosed: %q", private)
		}
	}
	if report.ProductionReady || report.CredentialsRead || report.NetworkContacted || report.ConfigurationModified {
		t.Fatal("incorrect side effect or readiness claim")
	}
	if status(t, report, "configuration") != "pass" || status(t, report, "telemetry") != "not_checked" {
		t.Fatal("incorrect default checks")
	}
}
func TestDoctorSanitizesConfigurationFailure(t *testing.T) {
	d := deps(config.Config{}, agent.Telemetry{})
	d.load = func(string) (config.Config, error) { return config.Config{}, errors.New("private-path-and-secret") }
	d.probe = func(context.Context) agent.Telemetry { t.Fatal("probe after failure"); return agent.Telemetry{} }
	report := inspect(context.Background(), "ignored", true, false, d)
	b, _ := json.Marshal(report)
	if strings.Contains(string(b), "private-path-and-secret") || report.ResourcePolicy != nil || report.Mode != "unknown" {
		t.Fatal("configuration failure was not sanitized")
	}
	if status(t, report, "configuration") != "blocked" {
		t.Fatal("missing failure")
	}
}
func TestDoctorRevalidatesConfiguration(t *testing.T) {
	c := validConfig()
	c.MarketplaceEnabled = true
	report := inspect(context.Background(), "ignored", false, false, deps(c, agent.Telemetry{}))
	if status(t, report, "configuration") != "blocked" {
		t.Fatal("unsafe config accepted")
	}
}
func TestDoctorPlatformAndCancellationPreventProbe(t *testing.T) {
	for _, platform := range []string{"linux", "cancelled", "intel"} {
		t.Run(platform, func(t *testing.T) {
			d := deps(validConfig(), agent.Telemetry{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch platform {
			case "linux":
				d.os = "linux"
			case "intel":
				d.arch = "amd64"
			default:
				cancel()
			}
			d.probe = func(context.Context) agent.Telemetry { t.Fatal("unexpected probe"); return agent.Telemetry{} }
			report := inspect(ctx, "ignored", true, false, d)
			if status(t, report, "telemetry") == "pass" {
				t.Fatal("false probe success")
			}
		})
	}
}
func TestDoctorTelemetryAndAdmissionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		telemetry                     agent.Telemetry
		memory, idle, telemetryStatus string
	}{
		{"available", agent.Telemetry{Known: true, TotalMemoryBytes: 24 << 30, AvailableMemoryBytes: 8 << 30, IdleSeconds: 300}, "pass", "pass", "pass"},
		{"owner_active", agent.Telemetry{Known: true, TotalMemoryBytes: 24 << 30, AvailableMemoryBytes: 8 << 30, IdleSeconds: 299}, "pass", "warning", "pass"},
		{"low_free", agent.Telemetry{Known: true, TotalMemoryBytes: 24 << 30, AvailableMemoryBytes: 1 << 30, IdleSeconds: 300}, "warning", "pass", "pass"},
		{"total_too_small", agent.Telemetry{Known: true, TotalMemoryBytes: 1 << 30, AvailableMemoryBytes: 1 << 30, IdleSeconds: 300}, "blocked", "pass", "pass"},
		{"unknown", agent.Telemetry{}, "", "", "warning"},
		{"synthetic", agent.Telemetry{Known: true, Synthetic: true, TotalMemoryBytes: 24 << 30}, "", "", "warning"},
		{"impossible", agent.Telemetry{Known: true, TotalMemoryBytes: 1 << 30, AvailableMemoryBytes: 2 << 30}, "", "", "warning"},
		{"oversized", agent.Telemetry{Known: true, TotalMemoryBytes: 1 << 41}, "", "", "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := deps(validConfig(), tc.telemetry)
			d.probe = func(ctx context.Context) agent.Telemetry {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing probe deadline")
				}
				return tc.telemetry
			}
			report := inspect(context.Background(), "ignored", true, false, d)
			if status(t, report, "telemetry") != tc.telemetryStatus {
				t.Fatal("telemetry result mismatch")
			}
			if tc.memory != "" && (status(t, report, "memory_headroom") != tc.memory || status(t, report, "owner_idle") != tc.idle) {
				t.Fatal("admission diagnostic mismatch")
			}
			if report.ProductionReady {
				t.Fatal("diagnostic must never authorize production")
			}
		})
	}
}
func TestDoctorDevelopmentUnenrolledAndResumed(t *testing.T) {
	c := validConfig()
	c.Development, c.Paused, c.HostID = true, false, ""
	report := inspect(context.Background(), "ignored", false, false, deps(c, agent.Telemetry{}))
	if report.Mode != "development" || status(t, report, "stored_identity") != "warning" || status(t, report, "saved_pause") != "warning" {
		t.Fatal("saved state not correctly described")
	}
}
func TestInspectReadsConfigWithoutCreatingCredentialsOrChangingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := config.Save(path, validConfig()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	report := Inspect(context.Background(), path, false, false)
	after, _ := os.ReadFile(path)
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 || string(before) != string(after) {
		t.Fatal("diagnostic modified configuration or created files")
	}
	if status(t, report, "configuration") != "pass" {
		t.Fatal("config not read")
	}
}

// NAT observability, entirely offline: observeNAT is injected, exactly like the
// telemetry probe. No test in this package sends a STUN packet.
func natDeps(c config.Config, result stun.Result, err error) dependencies {
	d := deps(c, agent.Telemetry{})
	d.observeNAT = func(ctx context.Context) (stun.Result, error) {
		if _, ok := ctx.Deadline(); !ok {
			panic("the STUN observation must be deadline bounded")
		}
		return result, err
	}
	return d
}

func TestDoctorDoesNotQuerySTUNUnlessAsked(t *testing.T) {
	d := natDeps(validConfig(), stun.Result{}, nil)
	d.observeNAT = func(context.Context) (stun.Result, error) {
		t.Fatal("doctor sent a STUN query without --stun")
		return stun.Result{}, nil
	}
	report := inspect(context.Background(), "ignored", false, false, d)
	if report.NetworkContacted {
		t.Fatal("a default report must not claim network contact")
	}
	if report.NAT != nil {
		t.Fatal("no NAT block without --stun")
	}
	if status(t, report, "nat_reflexive") != "not_checked" {
		t.Fatal("the NAT check must report not_checked by default")
	}
}

func TestDoctorReportsReflexiveAddressAndMapping(t *testing.T) {
	result := stun.Result{Reachable: true,
		Reflexive:    netip.MustParseAddrPort("203.0.113.9:41234"),
		Mapping:      stun.MappingEndpointIndependent,
		MappingLabel: stun.MappingEndpointIndependent.String(),
		Summary:      stun.MappingEndpointIndependent.Summary(),
		Observations: []stun.Observation{{Server: "stun.cloudflare.com:3478"}, {Server: "turn.cloudflare.com:3478"}},
	}
	report := inspect(context.Background(), "ignored", false, true, natDeps(validConfig(), result, nil))
	if !report.NetworkContacted {
		t.Fatal("a STUN query is network contact and must be reported as such")
	}
	if report.NAT == nil || report.NAT.ReflexiveAddress != "203.0.113.9:41234" ||
		report.NAT.Mapping != "endpoint-independent" || len(report.NAT.Servers) != 2 {
		t.Fatalf("nat = %+v", report.NAT)
	}
	if status(t, report, "nat_reflexive") != "pass" || status(t, report, "nat_mapping") != "pass" {
		t.Fatal("an observed address and a comparable mapping should both pass")
	}
	// HONESTY: no check may claim a working peer-to-peer path, because none exists.
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hole punching will work", "peer-to-peer is available", "will work"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report promises more than was established: %q", forbidden)
		}
	}
	if !strings.Contains(string(encoded), "OBSERVABILITY ONLY") {
		t.Fatal("the report must say the observation establishes no connection")
	}
}

func TestDoctorReportsSymmetricNATAsAWarning(t *testing.T) {
	result := stun.Result{Reachable: true,
		Reflexive:    netip.MustParseAddrPort("203.0.113.9:1000"),
		Mapping:      stun.MappingEndpointDependent,
		MappingLabel: stun.MappingEndpointDependent.String(),
		Summary:      stun.MappingEndpointDependent.Summary()}
	report := inspect(context.Background(), "ignored", false, true, natDeps(validConfig(), result, nil))
	if status(t, report, "nat_mapping") != "warning" {
		t.Fatal("an endpoint-dependent mapping must warn: hole punching would fail")
	}
}

func TestDoctorReportsUnknownMappingWhenOnlyOneServerAnswered(t *testing.T) {
	result := stun.Result{Reachable: true,
		Reflexive:    netip.MustParseAddrPort("203.0.113.9:1000"),
		Mapping:      stun.MappingUnknown,
		MappingLabel: stun.MappingUnknown.String(), Summary: stun.MappingUnknown.Summary()}
	report := inspect(context.Background(), "ignored", false, true, natDeps(validConfig(), result, nil))
	if status(t, report, "nat_mapping") != "not_checked" {
		t.Fatal("one sample must not be reported as a classification")
	}
}

func TestDoctorReportsBlockedSTUN(t *testing.T) {
	result := stun.Result{Reachable: false, MappingLabel: "unknown", Summary: stun.MappingUnknown.Summary(),
		Observations: []stun.Observation{{Server: "stun.cloudflare.com:3478", Error: "i/o timeout"}}}
	report := inspect(context.Background(), "ignored", false, true, natDeps(validConfig(), result, nil))
	if status(t, report, "nat_reflexive") != "warning" || report.NAT == nil || report.NAT.Reachable {
		t.Fatalf("an unreachable STUN path must be a reported finding, not an error: %+v", report.NAT)
	}
	if report.NAT.ReflexiveAddress != "" {
		t.Fatal("no address may be reported when nothing answered")
	}
}

func TestDoctorReportsStaticPeerCountWithoutAddresses(t *testing.T) {
	c := validConfig()
	c.StaticPeers = []config.StaticPeer{{Endpoint: "https://10.20.0.5:8443",
		Fingerprint: "aa11223344556677889900112233445566778899001122334455667788990011",
		Label:       "studio vlan 20"}}
	report := inspect(context.Background(), "ignored", false, false, natDeps(c, stun.Result{}, nil))
	if status(t, report, "static_peers") != "pass" {
		t.Fatal("a configured static peer should be reported")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"10.20.0.5", "studio vlan 20", "aa1122"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("doctor leaked a configured value: %q", private)
		}
	}
	if !strings.Contains(string(encoded), "authorized only if the coordinator lists its fingerprint") {
		t.Fatal("the report must state that configuration is not authorization")
	}
}
