package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/agent"
	"nexal/connector/internal/config"
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
	report := inspect(context.Background(), "/private-config-path", false, d)
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
	report := inspect(context.Background(), "ignored", true, d)
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
	report := inspect(context.Background(), "ignored", false, deps(c, agent.Telemetry{}))
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
			report := inspect(ctx, "ignored", true, d)
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
			report := inspect(context.Background(), "ignored", true, d)
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
	report := inspect(context.Background(), "ignored", false, deps(c, agent.Telemetry{}))
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
	report := Inspect(context.Background(), path, false)
	after, _ := os.ReadFile(path)
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 || string(before) != string(after) {
		t.Fatal("diagnostic modified configuration or created files")
	}
	if status(t, report, "configuration") != "pass" {
		t.Fatal("config not read")
	}
}
