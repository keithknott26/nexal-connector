// Package diagnostics produces allowlisted, read-only setup reports. It has no
// credential-store, HTTP-client, tunnel-supervisor or job-runner dependency.
package diagnostics

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"nexal/connector/internal/agent"
	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/stun"
)

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // pass, warning, not_checked, blocked
	Message string `json:"message"`
}

// NAT is the STUN observation, present only when the owner asked for it with
// --stun. It is OBSERVABILITY AND NOTHING ELSE: it reports what the internet sees
// this host as and whether the NAT reuses one mapping, so a human can answer
// "would hole punching even work here". No code in this connector punches a hole,
// signals a candidate, or dials a public address — see TRANSPORT-NAT-DESIGN.md.
type NAT struct {
	// Reachable is false when no STUN server answered, which usually means UDP
	// 3478 is blocked or there is no network. That is a finding, not an error.
	Reachable bool `json:"reachable"`
	// ReflexiveAddress is this host's public address:port as one server saw it,
	// empty when nothing answered. It is the owner's own address, printed for the
	// owner on their own machine; unlike host names, host IDs and paths, the whole
	// point of the check is to show it.
	ReflexiveAddress string `json:"reflexiveAddress,omitempty"`
	// Mapping is endpoint-independent, endpoint-dependent or unknown. Unknown is
	// a real answer and is what one sample yields.
	Mapping string `json:"mapping"`
	// Summary states plainly what the classification does and does not imply.
	Summary string `json:"summary"`
	// Servers is the per-server outcome so a failure names itself.
	Servers []stun.Observation `json:"servers"`
}

type Report struct {
	SchemaVersion         int                    `json:"schemaVersion"`
	ConnectorVersion      string                 `json:"connectorVersion"`
	Mode                  string                 `json:"mode"`
	Checks                []Check                `json:"checks"`
	ResourcePolicy        *config.ResourcePolicy `json:"resourcePolicy,omitempty"`
	NAT                   *NAT                   `json:"nat,omitempty"`
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
	// observeNAT is injected so every test in this package stays offline, the
	// same way probe already is.
	observeNAT func(context.Context) (stun.Result, error)
}

// Inspect never prints config values such as host name, host ID, endpoint or
// filesystem paths. --probe is an explicit request to run the three bounded
// macOS telemetry commands; otherwise only the configuration file is read.
// It does not inspect Keychain, tunnel tokens, or credential files.
//
// nat is an explicit request to send STUN binding requests, which is the ONLY
// thing in this report that touches the network. It is opt-in for that reason,
// and it sets NetworkContacted so the report never claims an offline run it did
// not have.
func Inspect(ctx context.Context, path string, probe, nat bool) Report {
	return inspect(ctx, path, probe, nat, dependencies{
		load: config.Load, probe: agent.MacProbe(0), os: runtime.GOOS, arch: runtime.GOARCH,
		observeNAT: func(ctx context.Context) (stun.Result, error) {
			return (&stun.Client{}).Observe(ctx)
		},
	})
}

func inspect(ctx context.Context, path string, probe, nat bool, d dependencies) Report {
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
	// Static cross-VLAN peers. The count is reported, never the addresses: an
	// endpoint is a config value, and this report does not print those. The
	// message says the quiet part out loud, because "I configured a peer" is the
	// setting most likely to be mistaken for "I authorized a peer".
	if valid {
		switch n := len(c.StaticPeers); n {
		case 0:
			add("static_peers", "not_checked", "No static peer endpoints are configured. Peers on another VLAN cannot be found by mDNS, which is link-local; configure a static peer or enable mDNS reflection on the switch.")
		default:
			add("static_peers", "pass", fmt.Sprintf("%d static peer endpoint(s) are configured and passed the private-IP endpoint rule. Reachability was NOT tested, and a configured peer is authorized only if the coordinator lists its fingerprint.", n))
		}
	}
	if !nat {
		add("nat_reflexive", "not_checked", "No STUN query was sent. Use doctor --stun to observe this host's public address and NAT mapping behaviour.")
	} else {
		report.NetworkContacted = true
		natCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		result, err := d.observeNAT(natCtx)
		cancel()
		switch {
		case err != nil:
			add("nat_reflexive", "warning", "The STUN observation could not be started. No NAT conclusion is implied.")
		case !result.Reachable:
			report.NAT = &NAT{Reachable: false, Mapping: result.MappingLabel,
				Summary: result.Summary, Servers: result.Observations}
			add("nat_reflexive", "warning", "No STUN server answered, so this host's public address is unknown. UDP 3478 is probably blocked outbound. Peer transport on a local or routed private network is unaffected.")
		default:
			report.NAT = &NAT{Reachable: true, ReflexiveAddress: result.Reflexive.String(),
				Mapping: result.MappingLabel, Summary: result.Summary, Servers: result.Observations}
			// Deliberately "pass" for the observation and nothing stronger: knowing
			// the address is not being reachable at it. There is no hole punching,
			// no candidate signalling and no relay in this connector.
			add("nat_reflexive", "pass", "A public reflexive address was observed. This is OBSERVABILITY ONLY: no connection was established, no address was published, and this connector cannot yet dial a peer over the internet.")
			if result.Mapping == stun.MappingEndpointDependent {
				add("nat_mapping", "warning", "This NAT allocated a different mapping per destination (endpoint-dependent/symmetric). If peer-to-peer over the internet is implemented, this host will need a relay rather than a hole punch.")
			} else if result.Mapping == stun.MappingUnknown {
				add("nat_mapping", "not_checked", "Fewer than two STUN servers answered, so NAT mapping behaviour could not be compared. One sample is never a classification.")
			} else {
				add("nat_mapping", "pass", "Two servers saw the same mapping (endpoint-independent), so hole punching is plausible. It is NOT proven and is not implemented: only a real simultaneous dial would prove it.")
			}
		}
	}
	add("credentials", "not_checked", "Keychain, admin/host credentials and tunnel token files were not accessed.")
	if report.NetworkContacted {
		add("connectivity", "not_checked", "No coordinator or local API request was made. The only network traffic was the explicitly requested STUN binding query, which sends no credential and no host identity.")
	} else {
		add("connectivity", "not_checked", "No coordinator, local API, DNS or external network request was made.")
	}
	add("production_dispatch", "blocked", "Production marketplace dispatch remains disabled pending isolation, signed dispatch and verified tunnel acceptance.")
	return report
}
