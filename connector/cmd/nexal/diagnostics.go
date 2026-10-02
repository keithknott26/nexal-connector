package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nexal/connector/internal/agent"
	"nexal/connector/internal/config"
	"nexal/connector/internal/diaglog"
	"nexal/connector/internal/mesh"
	"nexal/connector/internal/sysinfo"
	"nexal/connector/internal/wol"
)

// diagnosticsCommand is `nexal diagnostics [--on|--off|--status|--bundle <path>]`.
//
// --on/--off create or remove the diagnostics.enabled flag next to config.json;
// a running agent applies it within diaglog.PollInterval, without a restart.
// --bundle writes a redacted plain-text bundle for the owner to read or share.
// Every form prints the current state as JSON.
func diagnosticsCommand(ctx context.Context, args []string) error {
	f, path, err := flags("diagnostics")
	if err != nil {
		return err
	}
	on := f.Bool("on", false, "turn diagnostic mode on")
	off := f.Bool("off", false, "turn diagnostic mode off")
	_ = f.Bool("status", false, "report diagnostic mode (the default)")
	bundle := f.String("bundle", "", "write a redacted .txt diagnostics bundle to this absolute path")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if *on && *off {
		return errors.New("choose one of --on or --off")
	}
	switch {
	case *on:
		err = diaglog.SetEnabled(*path, true)
	case *off:
		err = diaglog.SetEnabled(*path, false)
	}
	if err != nil {
		return err
	}
	state := diagnosticsState(*path)
	if *bundle != "" {
		if !filepath.IsAbs(*bundle) {
			return errors.New("--bundle must be an absolute path")
		}
		text := buildDiagnosticsBundle(ctx, *path, liveBundleSources(*path))
		if err := os.WriteFile(*bundle, []byte(text), 0o600); err != nil {
			return errors.New("cannot write the diagnostics bundle")
		}
		state["bundle"] = *bundle
		state["bundleBytes"] = len(text)
	}
	return emit(state)
}

func diagnosticsState(configPath string) map[string]any {
	dir, _ := diaglog.LogDir()
	return map[string]any{
		"enabled":        diaglog.Enabled(configPath),
		"logDirectory":   dir,
		"agentLog":       filepath.Join(dir, diaglog.AgentLogName),
		"diagnosticsLog": filepath.Join(dir, diaglog.FileName),
		"appLog":         filepath.Join(dir, diaglog.AppLogName),
		"pollSeconds":    int(diaglog.PollInterval / time.Second),
	}
}

// bundleSources are the inputs of a bundle, replaced in tests.
type bundleSources struct {
	Now     func() time.Time
	Status  func(ctx context.Context) ([]byte, error) // the local API /v1/status body
	Runtime func(ctx context.Context) (mesh.RuntimeDetail, error)
	Wake    func(ctx context.Context) wol.Facts
	Host    func(ctx context.Context) sysinfo.Info
	LogDir  string
}

func liveBundleSources(configPath string) bundleSources {
	dir, _ := diaglog.LogDir()
	return bundleSources{
		Now: time.Now,
		Status: func(ctx context.Context) ([]byte, error) {
			return localFetch(ctx, configPath, "GET", "status", nil, 4<<20)
		},
		Runtime: mesh.ReadRuntimeDetail,
		Wake:    wol.CollectLocal,
		Host:    func(ctx context.Context) sysinfo.Info { return sysinfo.NewCollector().Collect(ctx) },
		LogDir:  dir,
	}
}

// Bundle tail sizes. The whole bundle stays small enough to attach to a message.
const (
	bundleAgentTail = 256 << 10
	bundleDiagTail  = 768 << 10
	bundleAppTail   = 128 << 10
)

// buildDiagnosticsBundle never fails: a section whose source is unavailable
// says so. The finished text passes through diaglog.Redact as a whole.
func buildDiagnosticsBundle(ctx context.Context, configPath string, src bundleSources) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	section := func(title string) { line("\n== %s ==", title) }
	step := func(d time.Duration) (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, d) }

	line("neXal connector diagnostics bundle")
	line("generated: %s", src.Now().UTC().Format(time.RFC3339))
	line("note: secrets, tokens, setup keys, MAC addresses and long key-like strings are redacted")

	section("Versions")
	line("connector: %s", config.Version)
	line("go: %s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if src.Host != nil {
		hctx, cancel := step(8 * time.Second)
		h := src.Host(hctx)
		cancel()
		line("os: %s", h.OS)
		line("model: %s  chip: %s  cores: %d  memoryGiB: %d", h.Model, h.Chip, h.Cores, h.MemoryBytes>>30)
	}
	line("diagnosticMode: %t", diaglog.Enabled(configPath))
	if c, err := config.Load(configPath); err == nil {
		line("development: %t  enrolled: %t  guest: %t", c.Development, c.Enrollment != nil, c.GuestAccess != nil)
	} else {
		line("config: unreadable")
	}

	section("Mesh runtime (nexal-network status)")
	var selfAddress string
	if src.Runtime != nil {
		rctx, cancel := step(6 * time.Second)
		d, err := src.Runtime(rctx)
		cancel()
		if err != nil {
			line("unavailable: %s", err.Error())
		} else {
			selfAddress = d.SelfAddress
			line("runtimeVersion: daemon=%s cli=%s", d.DaemonVersion, d.CLIVersion)
			line("management: %t  signal: %t  kernelInterface: %t  quantumResistance: %t",
				d.ManagementConnected, d.SignalConnected, d.KernelInterface, d.QuantumResistance)
			line("tunnelAddress: %s", d.SelfAddress)
			line("routes: %s", strings.Join(d.Networks, ", "))
			line("peers: %d", len(d.Peers))
			for _, p := range d.Peers {
				line("  - %s ip=%s conn=%s type=%s ice=%s/%s latencyMs=%.1f handshake=%s",
					p.FQDN, p.TunnelAddress, p.ConnStatus, p.ConnectionType, p.ICELocal, p.ICERemote, p.LatencyMS, p.LastHandshake)
				line("    pq=%s reason=%s profile=%s keyInstalled=%s keyExpires=%s",
					p.PQ, p.PQReason, p.QuantumProfile, p.QuantumKeyInstalledAt, p.QuantumKeyExpiresAt)
			}
		}
	}

	section("Agent status (local API)")
	var st agent.Status
	statusOK := false
	if src.Status != nil {
		sctx, cancel := step(6 * time.Second)
		raw, err := src.Status(sctx)
		cancel()
		switch {
		case err != nil:
			line("unavailable: %s", err.Error())
		case json.Unmarshal(raw, &st) != nil:
			line("unavailable: status is not readable JSON")
		default:
			statusOK = true
		}
	}
	if statusOK {
		line("version: %s  paused: %t  coordinatorHealthy: %t  credentialRejected: %t",
			st.Version, st.Paused, st.CoordinatorHealthy, st.CredentialRejected)
		line("mesh: provider=%t lifecycle=%s step=%s pq=%s peers=%d",
			st.Mesh.ProviderAvailable, st.Mesh.Lifecycle, st.Mesh.AuthenticationStep, st.Mesh.PQ, len(st.Mesh.Peers))
		for _, p := range st.Mesh.Peers {
			line("  - %s ip=%s lifecycle=%s path=%s via=%s latencyMs=%.1f pq=%s reason=%s profile=%s pqExpires=%s services=%s",
				p.Name, p.TunnelAddress, p.Lifecycle, p.Path, p.DirectVia, p.LatencyMS, p.PQ, p.PQReason,
				p.QuantumProfile, p.PQExpiresAt, strings.Join(p.Services, ","))
		}
		line("presence: connected=%t online=%d updatedAt=%s detail=%s",
			st.Presence.Connected, len(st.Presence.Online), st.Presence.UpdatedAt, st.Presence.Detail)
	}

	section("Wake-on-LAN")
	if statusOK {
		line("agent: macs=%d wakeForNetwork=%s lastReportAccepted=%t", len(st.Wake.MACs), st.Wake.WakeForNetwork, st.Wake.Reported)
	}
	if src.Wake != nil {
		wctx, cancel := step(5 * time.Second)
		f := src.Wake(wctx)
		cancel()
		line("local: macs=%d lanKeyPrefixes=%d lanKeyPresent=%t wakeForNetwork=%s",
			len(f.MACs), len(f.Prefixes), f.LANKey != "", f.WakeForNetwork)
	}
	line("tunnelAddress: %s", selfAddress)

	for _, l := range []struct {
		name string
		max  int64
	}{{diaglog.AgentLogName, bundleAgentTail}, {diaglog.FileName, bundleDiagTail}, {diaglog.AppLogName, bundleAppTail}} {
		section(fmt.Sprintf("%s (last %d KiB)", l.name, l.max>>10))
		if src.LogDir == "" {
			line("(log directory unknown)")
			continue
		}
		tail := diaglog.Tail(filepath.Join(src.LogDir, l.name), l.max)
		if tail == "" {
			line("(empty or missing)")
			continue
		}
		b.WriteString(tail)
		if !strings.HasSuffix(tail, "\n") {
			b.WriteString("\n")
		}
	}
	return diaglog.Redact(b.String())
}
