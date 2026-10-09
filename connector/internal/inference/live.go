package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---- Connector status (host and link facts) --------------------------------

// LocalInfo is what the CLI collects locally for THIS Mac (a subset of
// sysinfo.Info, so this package stays free of that dependency).
type LocalInfo struct {
	Name          string
	OS            string
	Chip          string
	MemoryBytes   uint64
	DiskFreeBytes uint64
	DiskKnown     bool
}

// statusDoc is the subset of the connector's GET /v1/status that the preflight
// reads. Field names are the connector's JSON contract (internal/agent.Status,
// internal/mesh.Status, internal/agent.PresenceStatus, internal/config.ResourcePolicy).
type statusDoc struct {
	HostID           string `json:"hostId"`
	ExecutionBlocker string `json:"executionBlocker"`
	Paused           bool   `json:"paused"`
	Telemetry        struct {
		Known                bool   `json:"known"`
		TotalMemoryBytes     uint64 `json:"totalMemoryBytes"`
		AvailableMemoryBytes uint64 `json:"availableMemoryBytes"`
	} `json:"telemetry"`
	ResourcePolicy struct {
		MemoryLimitBytes   uint64 `json:"memoryLimitBytes"`
		ReserveMemoryBytes uint64 `json:"reserveMemoryBytes"`
		MinFreeDiskBytes   uint64 `json:"minFreeDiskBytes"`
	} `json:"resourcePolicy"`
	Mesh struct {
		ProviderAvailable bool       `json:"providerAvailable"`
		PQ                string     `json:"pq"`
		Peers             []meshPeer `json:"peers"`
	} `json:"mesh"`
	Presence struct {
		Hosts []presenceHost `json:"hosts"`
	} `json:"presence"`
}

type meshPeer struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Lifecycle         string  `json:"lifecycle"`
	Path              string  `json:"path"`
	PathLabel         string  `json:"pathLabel"`
	LatencyMS         float64 `json:"latencyMs"`
	PacketLossPercent float64 `json:"packetLossPercent"`
	PQ                string  `json:"pq"`
	PQReason          string  `json:"pqReason"`
	PathFlapsLastHour int     `json:"pathFlapsLastHour"`
	TunnelAddress     string  `json:"tunnelAddress"`
	DirectVia         string  `json:"directVia"`
	BandwidthMbps     float64 `json:"bandwidthMbps"`
}

type presenceHost struct {
	HostID string `json:"hostId"`
	Online bool   `json:"online"`
	Info   struct {
		Name                 string  `json:"name"`
		OS                   string  `json:"os"`
		Chip                 string  `json:"chip"`
		MemoryBytes          uint64  `json:"memoryBytes"`
		MemoryAvailableBytes *uint64 `json:"memoryAvailableBytes"`
		DiskFreeBytes        uint64  `json:"diskFreeBytes"`
		TunnelAddress        string  `json:"tunnelAddress"`
	} `json:"info"`
}

// StatusSource implements HostSource, LinkSource and ExtrasSource from one
// connector status document. It performs no I/O of its own.
type StatusSource struct {
	doc       statusDoc
	reachable bool
	err       error
	local     LocalInfo
}

// NewStatusSource decodes the status bytes. raw may be nil with fetchErr set when
// the connector could not be reached; the source then reports this Mac from local
// facts only and marks its memory policy unavailable.
func NewStatusSource(raw []byte, fetchErr error, local LocalInfo) *StatusSource {
	s := &StatusSource{local: local, err: fetchErr}
	if fetchErr == nil {
		if err := json.Unmarshal(raw, &s.doc); err != nil {
			s.err = errors.New("the connector's status was not valid JSON")
		} else {
			s.reachable = true
		}
	}
	return s
}

func isGateway(name string) bool { return strings.HasPrefix(strings.ToLower(name), "gw-") }

func (s *StatusSource) Extras() StatusExtras {
	return StatusExtras{Reachable: s.reachable, ExecutionBlocker: s.doc.ExecutionBlocker, Paused: s.doc.Paused,
		MeshPQ: map[bool]string{true: s.doc.Mesh.PQ, false: ""}[s.doc.Mesh.ProviderAvailable]}
}

func (s *StatusSource) Hosts(ctx context.Context) ([]Host, error) {
	self := Host{ID: s.doc.HostID, Name: s.local.Name, IsSelf: true, Online: true, Chip: s.local.Chip, OS: s.local.OS,
		DiskFreeBytes: s.local.DiskFreeBytes, DiskKnown: s.local.DiskKnown, TotalMemoryBytes: s.local.MemoryBytes}
	if self.ID == "" {
		self.ID = "this-mac"
	}
	if self.Name == "" {
		self.Name = "This Mac"
	}
	if s.reachable {
		if s.doc.Telemetry.TotalMemoryBytes > 0 {
			self.TotalMemoryBytes = s.doc.Telemetry.TotalMemoryBytes
		}
		self.AvailableMemoryBytes = s.doc.Telemetry.AvailableMemoryBytes
		self.MemoryKnown = s.doc.Telemetry.Known && self.TotalMemoryBytes > 0
		self.AdmissionKnown = s.doc.ResourcePolicy.MemoryLimitBytes > 0
		self.ApprovedMemoryBytes = s.doc.ResourcePolicy.MemoryLimitBytes
		self.OwnerReserveBytes = s.doc.ResourcePolicy.ReserveMemoryBytes
		self.DiskReserveBytes = s.doc.ResourcePolicy.MinFreeDiskBytes
	}
	hosts := []Host{self}
	for _, p := range s.doc.Mesh.Peers {
		if isGateway(p.Name) {
			continue
		}
		h := Host{ID: p.ID, Name: p.Name, Online: p.Lifecycle == "connected" || p.Lifecycle == "degraded"}
		for _, ph := range s.doc.Presence.Hosts {
			if p.TunnelAddress != "" && ph.Info.TunnelAddress == p.TunnelAddress {
				h.Chip, h.OS, h.TotalMemoryBytes = ph.Info.Chip, ph.Info.OS, ph.Info.MemoryBytes
				if ph.Info.MemoryAvailableBytes != nil && ph.Info.MemoryBytes > 0 {
					h.AvailableMemoryBytes, h.MemoryKnown = *ph.Info.MemoryAvailableBytes, true
				}
				h.DiskFreeBytes, h.DiskKnown = ph.Info.DiskFreeBytes, ph.Info.DiskFreeBytes > 0
				break
			}
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func (s *StatusSource) Links(ctx context.Context) ([]Link, error) {
	var out []Link
	for _, p := range s.doc.Mesh.Peers {
		if isGateway(p.Name) {
			continue
		}
		out = append(out, Link{PeerID: p.ID, Lifecycle: p.Lifecycle, Path: p.Path, PathLabel: p.PathLabel, DirectVia: p.DirectVia,
			LatencyMS: p.LatencyMS, LatencyKnown: p.LatencyMS > 0,
			BandwidthMbps: p.BandwidthMbps, BandwidthKnown: p.BandwidthMbps > 0,
			PacketLossPercent: p.PacketLossPercent, LossKnown: p.PacketLossPercent > 0,
			PQ: p.PQ, PQReason: p.PQReason, PathFlapsLastHour: p.PathFlapsLastHour})
	}
	return out, nil
}

// ---- MLX runtime probe -------------------------------------------------------

// Candidate pins, mirroring runtimes/nexal_mlx/runtime.py CANDIDATE_PINS.
var runtimePins = map[string]string{"mlx": "0.29.3", "mlx-lm": "0.28.4", "transformers": "4.57.6"}

var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ownerPolicy is the subset of the runtime bridge's owner-policy.json the probe
// needs (runtimes/bridge/runner.go OwnerPolicy). Config/model fields are not read.
type ownerPolicy struct {
	Installation struct {
		Python string `json:"python"`
		Entry  string `json:"entry"`
		Config string `json:"config"`
	} `json:"installation"`
	EntrySHA256        string            `json:"entry_sha256"`
	PythonSHA256       string            `json:"python_sha256"`
	ConfigSHA256       string            `json:"config_sha256"`
	RuntimeFilesSHA256 map[string]string `json:"runtime_files_sha256"`
}

// Runner executes the fixed probe command and returns its stdout.
type Runner func(ctx context.Context, python, entry string) ([]byte, error)

// FileProber probes the runtime named by an owner policy file. It runs ONLY
// `python -I -B <entry> probe`, and only after the interpreter and every runtime
// source file match the SHA-256 pins in the policy. It never passes
// --load-backend, so MLX is not imported and Metal is not touched.
type FileProber struct {
	OwnerPolicyPath string
	Timeout         time.Duration
	Run             Runner // nil = real exec
}

type probeOut struct {
	SchemaVersion        int               `json:"schema_version"`
	RuntimeVersion       string            `json:"runtime_version"`
	System               string            `json:"system"`
	Machine              string            `json:"machine"`
	Packages             map[string]string `json:"packages"`
	DistributedExecution string            `json:"distributed_execution"`
}

func sha256File(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return "", errors.New("unreadable or too large")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p FileProber) Probe(ctx context.Context) RuntimeProbe {
	fail := func(state, detail string) RuntimeProbe { return RuntimeProbe{State: state, Detail: detail} }
	if p.OwnerPolicyPath == "" {
		return fail(RuntimeNotConfigured, "No runtime owner policy was given.")
	}
	raw, err := os.ReadFile(p.OwnerPolicyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fail(RuntimeNotConfigured, "No runtime owner policy exists at the expected location, so the runtime was never provisioned here.")
	}
	if err != nil || len(raw) > 64*1024 {
		return fail(RuntimeProbeFailed, "The runtime owner policy could not be read.")
	}
	var pol ownerPolicy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&pol) != nil || !digestRE.MatchString(pol.PythonSHA256) || !digestRE.MatchString(pol.EntrySHA256) {
		return fail(RuntimeProbeFailed, "The runtime owner policy is malformed.")
	}
	in := pol.Installation
	for _, path := range []string{in.Python, in.Entry} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return fail(RuntimeProbeFailed, "The runtime owner policy has an unsafe path.")
		}
	}
	if got, err := sha256File(in.Python, 128<<20); err != nil || got != pol.PythonSHA256 {
		return fail(RuntimeIntegrityMismatch, "The Python interpreter does not match the owner's pinned SHA-256.")
	}
	if err := verifyRuntimeTree(in.Entry, pol); err != nil {
		return fail(RuntimeIntegrityMismatch, err.Error())
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := p.Run
	if run == nil {
		run = execProbe
	}
	out, err := run(cctx, in.Python, in.Entry)
	if err != nil {
		return fail(RuntimeProbeFailed, "The runtime probe did not complete.")
	}
	return interpretProbe(out)
}

// verifyRuntimeTree requires every file under the entry's directory to be a
// regular .py file pinned in the policy (entry plus nexal_mlx/*), with no symlinks.
func verifyRuntimeTree(entry string, pol ownerPolicy) error {
	root := filepath.Dir(entry)
	if len(pol.RuntimeFilesSHA256) < 2 || len(pol.RuntimeFilesSHA256) > 256 || pol.RuntimeFilesSHA256[filepath.Base(entry)] != pol.EntrySHA256 {
		return errors.New("The owner policy does not pin the runtime entry file.")
	}
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.Type()&fs.ModeSymlink != 0 {
			return errors.New("unsafe")
		}
		if d.IsDir() {
			if path != root && path != filepath.Join(root, "nexal_mlx") {
				return errors.New("unexpected directory")
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		want, ok := pol.RuntimeFilesSHA256[rel]
		if !ok || !digestRE.MatchString(want) || !strings.HasSuffix(rel, ".py") || !d.Type().IsRegular() {
			return errors.New("unpinned file")
		}
		got, err := sha256File(path, 8<<20)
		if err != nil || got != want {
			return errors.New("digest mismatch")
		}
		count++
		return nil
	})
	if err != nil || count != len(pol.RuntimeFilesSHA256) {
		return errors.New("The runtime files do not match the owner's pinned SHA-256 list.")
	}
	return nil
}

// limitedBuffer is deliberately NOT an embedded bytes.Buffer: that would promote
// ReadFrom, and io.Copy would then bypass this Write and its limit.
type limitedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.over = true
		return 0, errors.New("output limit exceeded")
	}
	return b.buf.Write(p)
}

func execProbe(ctx context.Context, python, entry string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, python, "-I", "-B", entry, "probe")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "PYTHONDONTWRITEBYTECODE=1"}
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	out := &limitedBuffer{max: 64 << 10}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil || out.over {
		return nil, errors.New("probe failed or exceeded its output limit")
	}
	return out.buf.Bytes(), nil
}

// interpretProbe turns probe stdout into a RuntimeProbe state.
func interpretProbe(out []byte) RuntimeProbe {
	var po probeOut
	if err := json.Unmarshal(out, &po); err != nil || po.SchemaVersion != 1 {
		return RuntimeProbe{State: RuntimeProbeFailed, Detail: "The runtime probe returned output this check does not understand."}
	}
	rp := RuntimeProbe{RuntimeVersion: po.RuntimeVersion, System: po.System, Machine: po.Machine,
		MLXVersion: po.Packages["mlx"], MLXLMVersion: po.Packages["mlx-lm"], TransformersVersion: po.Packages["transformers"],
		DistributedExecution: po.DistributedExecution}
	if po.System != "Darwin" || po.Machine != "arm64" {
		rp.State, rp.Detail = RuntimeUnsupportedPlatform, fmt.Sprintf("The runtime reports %s/%s; MLX needs Darwin/arm64.", po.System, po.Machine)
		return rp
	}
	var missing, wrong []string
	for _, name := range []string{"mlx", "mlx-lm", "transformers"} {
		switch v := po.Packages[name]; {
		case v == "":
			missing = append(missing, name)
		case v != runtimePins[name]:
			wrong = append(wrong, fmt.Sprintf("%s %s (pinned %s)", name, v, runtimePins[name]))
		}
	}
	switch {
	case len(missing) > 0:
		rp.State, rp.Detail = RuntimeDependenciesMissing, "Not installed: "+strings.Join(missing, ", ")+"."
	case len(wrong) > 0:
		rp.State, rp.Detail = RuntimeVersionMismatch, "Installed: "+strings.Join(wrong, ", ")+"."
	default:
		rp.State = RuntimeHealthy
		rp.Detail = "The probe ran and the package versions match the pins. This does not check the dependency receipt, Metal or any model."
	}
	return rp
}
