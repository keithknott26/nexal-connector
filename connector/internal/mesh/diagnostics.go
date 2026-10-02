package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// RuntimeDetail is the diagnostic view of `nexal-network status --json`: the
// runtime's own version, service state, routes and per-peer link and
// post-quantum evidence. It never carries setup keys, private keys, WireGuard
// public keys or the peers' ICE endpoint addresses.
type RuntimeDetail struct {
	DaemonVersion       string              `json:"daemonVersion"`
	CLIVersion          string              `json:"cliVersion"`
	ManagementConnected bool                `json:"managementConnected"`
	SignalConnected     bool                `json:"signalConnected"`
	KernelInterface     bool                `json:"kernelInterface"`
	QuantumResistance   bool                `json:"quantumResistance"`
	SelfAddress         string              `json:"selfAddress"`
	Networks            []string            `json:"networks"`
	Peers               []RuntimePeerDetail `json:"peers"`
}

// RuntimePeerDetail is one peer in RuntimeDetail.
type RuntimePeerDetail struct {
	FQDN                  string  `json:"fqdn"`
	TunnelAddress         string  `json:"tunnelAddress"`
	ConnStatus            string  `json:"connStatus"`
	ConnectionType        string  `json:"connectionType"`
	ICELocal              string  `json:"iceLocal"`
	ICERemote             string  `json:"iceRemote"`
	LatencyMS             float64 `json:"latencyMs"`
	LastHandshake         string  `json:"lastHandshake,omitempty"`
	QuantumProfile        string  `json:"quantumProfile,omitempty"`
	QuantumKeyInstalledAt string  `json:"quantumKeyInstalledAt,omitempty"`
	QuantumKeyExpiresAt   string  `json:"quantumKeyExpiresAt,omitempty"`
	PQ                    PQState `json:"pq"`
	PQReason              string  `json:"pqReason,omitempty"`
}

// runtimeExtras are status fields read only for diagnostics. They are decoded
// separately and leniently, so a runtime that reshapes one of them can never
// break translateRuntime.
type runtimeExtras struct {
	DaemonVersion string `json:"daemonVersion"`
	CLIVersion    string `json:"cliVersion"`
	Signal        struct {
		Connected bool `json:"connected"`
	} `json:"signal"`
	UsesKernelInterface bool     `json:"usesKernelInterface"`
	Networks            []string `json:"networks"`
}

// ReadRuntimeDetail runs the installed runtime's status command once.
func ReadRuntimeDetail(ctx context.Context) (RuntimeDetail, error) {
	out, err := runRuntimeStatus(ctx)
	if err != nil {
		return RuntimeDetail{}, errors.New(runtimeFailureReason(err))
	}
	return parseRuntimeDetail(out, time.Now())
}

func parseRuntimeDetail(out []byte, now time.Time) (RuntimeDetail, error) {
	var rs runtimeStatus
	if err := json.Unmarshal(out, &rs); err != nil {
		return RuntimeDetail{}, errors.New("runtime status is not readable JSON")
	}
	var ex runtimeExtras
	_ = json.Unmarshal(out, &ex) // best effort; see runtimeExtras
	d := RuntimeDetail{DaemonVersion: ex.DaemonVersion, CLIVersion: ex.CLIVersion,
		ManagementConnected: rs.Management.Connected, SignalConnected: ex.Signal.Connected,
		KernelInterface: ex.UsesKernelInterface, QuantumResistance: rs.QuantumResistance,
		Networks: slices.Clone(ex.Networks), Peers: []RuntimePeerDetail{}}
	if d.Networks == nil {
		d.Networks = []string{}
	}
	sort.Strings(d.Networks)
	d.SelfAddress, _, _ = strings.Cut(rs.NetbirdIP, "/")
	for _, rp := range rs.Peers.Details {
		p := RuntimePeerDetail{FQDN: rp.FQDN, ConnStatus: strings.ToLower(rp.Status),
			ConnectionType: strings.ToLower(rp.ConnectionType), ICELocal: rp.ICECandidateType.Local,
			ICERemote: rp.ICECandidateType.Remote, QuantumProfile: rp.QuantumProfile,
			QuantumKeyInstalledAt: rp.QuantumKeyInstalledAt, QuantumKeyExpiresAt: rp.QuantumKeyExpiresAt}
		p.TunnelAddress, _, _ = strings.Cut(rp.NetbirdIP, "/")
		if t, err := time.Parse(time.RFC3339Nano, rp.LastHandshake); err == nil && t.Year() > 2000 {
			p.LastHandshake = t.UTC().Format(time.RFC3339)
		}
		var ns float64
		if json.Unmarshal(rp.Latency, &ns) == nil && ns > 0 {
			p.LatencyMS = math.Round(ns/1e5) / 10
		}
		lifecycle := LifecycleUnavailable
		switch p.ConnStatus {
		case "connected":
			lifecycle = LifecycleConnected
		case "connecting":
			lifecycle = LifecycleAuthenticating
		}
		_, verified := validQuantumEvidence(rp.QuantumProfile, rp.QuantumKeyInstalledAt, rp.QuantumKeyExpiresAt, now)
		if lifecycle == LifecycleConnected && rs.QuantumResistance && verified {
			p.PQ = PQProtected
		} else {
			p.PQ = PQDegraded
			p.PQReason = pqReason(rp, lifecycle, rs.QuantumResistance, now)
		}
		d.Peers = append(d.Peers, p)
	}
	sort.Slice(d.Peers, func(i, j int) bool { return d.Peers[i].FQDN < d.Peers[j].FQDN })
	return d, nil
}

func runtimeFailureReason(err error) string {
	var ee *exec.ExitError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "runtime status timed out"
	case errors.As(err, &ee):
		return fmt.Sprintf("runtime status exited %d", ee.ExitCode())
	case errors.Is(err, exec.ErrNotFound):
		return "runtime not installed"
	}
	return "runtime status failed: " + err.Error()
}

// ---- change logging for diagnostic mode ----

var diag struct {
	mu          sync.Mutex
	logger      *slog.Logger
	last        *RuntimeDetail
	lastFailure string
	services    map[string]string
}

// SetDiagnosticLogger installs the logger the runtime provider and service
// probes use to record changes. Nil (the default) records nothing.
func SetDiagnosticLogger(l *slog.Logger) {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	diag.logger = l
	diag.last = nil
	diag.lastFailure = ""
	diag.services = map[string]string{}
}

func diagLogger() *slog.Logger {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	return diag.logger
}

// noteRuntimeFailure logs a failed status poll: the first failure of a kind at
// Info, repeats at Debug.
func noteRuntimeFailure(err error) {
	l := diagLogger()
	if l == nil {
		return
	}
	reason := runtimeFailureReason(err)
	diag.mu.Lock()
	repeat := diag.lastFailure == reason
	diag.lastFailure = reason
	diag.mu.Unlock()
	if repeat {
		l.Debug("mesh runtime status poll failed", "reason", reason, "repeat", true)
		return
	}
	l.Info("mesh runtime status poll failed", "reason", reason)
}

func peerAttrs(p RuntimePeerDetail) []any {
	return []any{"fqdn", p.FQDN, "ip", p.TunnelAddress, "connStatus", p.ConnStatus,
		"connType", p.ConnectionType, "relayed", p.ConnectionType == "relayed",
		"iceLocal", p.ICELocal, "iceRemote", p.ICERemote, "latencyMs", p.LatencyMS}
}

func pqAttrs(p RuntimePeerDetail) []any {
	return []any{"fqdn", p.FQDN, "pq", string(p.PQ), "reason", p.PQReason, "profile", p.QuantumProfile,
		"keyInstalledAt", p.QuantumKeyInstalledAt, "keyExpiresAt", p.QuantumKeyExpiresAt}
}

// noteRuntimeStatus diffs a successful status poll against the previous one
// and logs what changed.
func noteRuntimeStatus(out []byte, now time.Time) {
	l := diagLogger()
	if l == nil {
		return
	}
	d, err := parseRuntimeDetail(out, now)
	if err != nil {
		l.Debug("mesh runtime status unreadable", "error", err.Error())
		return
	}
	diag.mu.Lock()
	prev := diag.last
	diag.last = &d
	recovered := diag.lastFailure != ""
	diag.lastFailure = ""
	diag.mu.Unlock()
	if recovered {
		l.Info("mesh runtime status readable again")
	}
	if prev == nil || prev.DaemonVersion != d.DaemonVersion || prev.CLIVersion != d.CLIVersion {
		l.Info("mesh runtime version", "daemon", d.DaemonVersion, "cli", d.CLIVersion)
	}
	if prev == nil || prev.ManagementConnected != d.ManagementConnected || prev.SignalConnected != d.SignalConnected ||
		prev.QuantumResistance != d.QuantumResistance || prev.SelfAddress != d.SelfAddress || prev.KernelInterface != d.KernelInterface {
		l.Info("mesh service state", "management", d.ManagementConnected, "signal", d.SignalConnected,
			"quantumResistance", d.QuantumResistance, "address", d.SelfAddress, "kernelInterface", d.KernelInterface,
			"peers", len(d.Peers))
	}
	if prev == nil || !slices.Equal(prev.Networks, d.Networks) {
		l.Info("mesh routes", "networks", d.Networks)
	}
	before := map[string]RuntimePeerDetail{}
	if prev != nil {
		for _, p := range prev.Peers {
			before[p.FQDN] = p
		}
	}
	for _, p := range d.Peers {
		old, known := before[p.FQDN]
		delete(before, p.FQDN)
		switch {
		case !known:
			l.Info("mesh peer added", peerAttrs(p)...)
		case old.ConnStatus != p.ConnStatus || old.ConnectionType != p.ConnectionType ||
			old.ICELocal != p.ICELocal || old.ICERemote != p.ICERemote || old.TunnelAddress != p.TunnelAddress:
			l.Debug("mesh peer connection changed", append(peerAttrs(p), "was", old.ConnStatus+"/"+old.ConnectionType)...)
		case math.Abs(old.LatencyMS-p.LatencyMS) >= 20:
			l.Debug("mesh peer latency changed", "fqdn", p.FQDN, "latencyMs", p.LatencyMS, "wasMs", old.LatencyMS)
		}
		switch {
		case !known || old.PQ != p.PQ || old.PQReason != p.PQReason:
			l.Debug("mesh peer pq state", pqAttrs(p)...)
		case old.QuantumKeyInstalledAt != p.QuantumKeyInstalledAt || old.QuantumProfile != p.QuantumProfile:
			l.Debug("mesh peer pq key rotated", pqAttrs(p)...)
		}
	}
	for fqdn := range before {
		l.Info("mesh peer removed", "fqdn", fqdn)
	}
}

// noteServiceProbe logs a service probe whose result differs from the last one
// for the same address.
func noteServiceProbe(ip string, services []string) {
	l := diagLogger()
	if l == nil {
		return
	}
	joined := strings.Join(services, ",")
	diag.mu.Lock()
	if diag.services == nil {
		diag.services = map[string]string{}
	}
	prev, seen := diag.services[ip]
	diag.services[ip] = joined
	diag.mu.Unlock()
	if seen && prev == joined {
		return
	}
	target := ip
	if ip == "127.0.0.1" {
		target = "local"
	}
	l.Debug("service probe", "target", target, "ssh", slices.Contains(services, "ssh"),
		"vnc", slices.Contains(services, "vnc"), "smb", slices.Contains(services, "smb"))
}
