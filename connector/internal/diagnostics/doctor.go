// Package diagnostics produces allowlisted, read-only setup reports. It has no
// credential-store, HTTP-client, tunnel-supervisor or job-runner dependency.
package diagnostics

import (
	"context"
	"runtime"
	"time"

	"nexal/connector/internal/agent"
	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // pass, warning, not_checked, blocked
	Message string `json:"message"`
}

type Report struct {
	SchemaVersion         int                    `json:"schemaVersion"`
	ConnectorVersion      string                 `json:"connectorVersion"`
	Mode                  string                 `json:"mode"`
	Checks                []Check                `json:"checks"`
	ResourcePolicy        *config.ResourcePolicy `json:"resourcePolicy,omitempty"`
	ProductionReady       bool                   `json:"productionReady"`
	CredentialsRead       bool                   `json:"credentialsRead"`
	NetworkContacted      bool                   `json:"networkContacted"`
	ConfigurationModified bool                   `json:"configurationModified"`
}

type dependencies struct {
	load  func(string) (config.Config, error)
	probe agent.Probe
	os    string
	arch  string
}

// Inspect never prints config values such as host name, host ID, endpoint or
// filesystem paths. --probe is an explicit request to run the three bounded
// macOS telemetry commands; otherwise only the configuration file is read.
// It does not inspect Keychain, tunnel tokens, or credential files.
func Inspect(ctx context.Context, path string, probe bool) Report {
	return inspect(ctx, path, probe, dependencies{
		load: config.Load, probe: agent.MacProbe(0), os: runtime.GOOS, arch: runtime.GOARCH,
	})
}

func inspect(ctx context.Context, path string, probe bool, d dependencies) Report {
	report := Report{SchemaVersion: 1, ConnectorVersion: config.Version, Mode: "unknown", Checks: []Check{}}
	add := func(id, status, message string) {
		report.Checks = append(report.Checks, Check{ID: id, Status: status, Message: message})
	}
	native := d.os == "darwin" && d.arch == "arm64"
	if native {
		add("native_platform", "pass", "Running on Apple Silicon macOS; this is not native release acceptance.")
	} else {
		add("native_platform", "warning", "Not running on Apple Silicon macOS; native Mac acceptance cannot be established here.")
	}
	c, err := d.load(path)
	valid := err == nil && c.Validate() == nil
	if !valid {
		// config.Load can include local paths in errors. Never forward them.
		add("configuration", "blocked", "Configuration is unavailable, unsafe or invalid. Review init and the private-file permissions in the setup guide.")
		add("stored_identity", "not_checked", "A valid configuration is required to inspect the stored enrollment marker.")
		add("saved_pause", "not_checked", "A valid configuration is required to inspect saved pause policy.")
		add("tunnel", "not_checked", "Tunnel configuration and live cryptographic negotiation were not verified.")
	} else {
		report.Mode = "production-gated"
		if c.Development {
			report.Mode = "development"
		}
		add("configuration", "pass", "Configuration schema, resource bounds and loopback binding are valid.")
		policy := c.ResourcePolicy()
		report.ResourcePolicy = &policy
		if client.ValidID(c.HostID) {
			add("stored_identity", "pass", "An enrollment marker is stored; credential validity and coordinator enrollment were not checked.")
		} else {
			add("stored_identity", "warning", "No valid enrollment marker is stored. Complete enrollment before starting the agent.")
		}
		if c.Paused {
			add("saved_pause", "pass", "Saved policy is paused. This report does not query or change the running agent.")
		} else {
			add("saved_pause", "warning", "Saved policy is resumed. Use status or pause to inspect or control the running agent.")
		}
		if c.Tunnel == nil {
			add("tunnel", "not_checked", "No tunnel is configured; none was started or downloaded.")
		} else {
			add("tunnel", "not_checked", "A tunnel configuration exists, but its executable, digest, token and negotiation were not inspected.")
		}
	}
	if !probe {
		add("telemetry", "not_checked", "Hardware telemetry was not read. Use doctor --probe for bounded local Mac telemetry checks.")
	} else if !valid || !native {
		add("telemetry", "not_checked", "Telemetry probing requires a valid configuration and Apple Silicon macOS.")
	} else if ctx.Err() != nil {
		add("telemetry", "warning", "Telemetry probing was cancelled; no admission decision is implied.")
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
		t := d.probe(probeCtx)
		expired := probeCtx.Err() != nil
		cancel()
		if expired || !t.Known || t.Synthetic || t.TotalMemoryBytes < 256<<20 ||
			t.TotalMemoryBytes > 1<<40 || t.AvailableMemoryBytes > t.TotalMemoryBytes {
			add("telemetry", "warning", "Trustworthy local memory and idle telemetry is unavailable; execution must remain fail-closed.")
		} else {
			add("telemetry", "pass", "Bounded local memory and idle observations succeeded; this is a point-in-time diagnostic, not authorization.")
			required := c.MemoryLimitBytes + c.ReserveMemoryBytes // validated bounded values, no overflow
			if required > t.TotalMemoryBytes {
				add("memory_headroom", "blocked", "Configured workload plus owner reserve exceeds physical RAM. Lower limits explicitly with set-policy.")
			} else if required > t.AvailableMemoryBytes {
				add("memory_headroom", "warning", "Current free and speculative memory does not cover workload plus owner reserve.")
			} else {
				add("memory_headroom", "pass", "Observed memory covers the configured workload and owner reserve at this instant.")
			}
			if t.IdleSeconds < c.IdleSeconds {
				add("owner_idle", "warning", "The owner idle threshold is not met. Owner-first scheduling should refuse new work.")
			} else {
				add("owner_idle", "pass", "The configured idle threshold is met at this instant; the agent must obtain its own fresh observations.")
			}
		}
	}
	add("credentials", "not_checked", "Keychain, admin/host credentials and tunnel token files were not accessed.")
	add("connectivity", "not_checked", "No coordinator, local API, DNS or external network request was made.")
	add("production_dispatch", "blocked", "Production marketplace dispatch remains disabled pending isolation, signed dispatch and verified tunnel acceptance.")
	return report
}
