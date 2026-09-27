package runtimebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Stable errors deliberately omit paths, subprocess output, prompts and causes.
var (
	ErrPlatform    = errors.New("local MLX inference requires Darwin arm64")
	ErrPolicy      = errors.New("owner runtime policy or reviewed file integrity rejected")
	ErrAdmission   = errors.New("existing local admission rejected")
	ErrInput       = errors.New("local inference input rejected")
	ErrProcess     = errors.New("local inference process failed")
	ErrOutputLimit = errors.New("local inference output limit exceeded")
	ErrResult      = errors.New("local inference result rejected")
	ErrBusy        = errors.New("local runtime configuration already has an active job")
)

const (
	DefaultStdoutBytes = 1024 * 1024
	DefaultStderrBytes = 64 * 1024
	maxTreeBytes       = 16 * 1024 * 1024
)

// OwnerPolicy is local deployment configuration, NOT a remotely supplied job.
// The entry must live in a dedicated immutable directory containing exactly the
// files in RuntimeFilesSHA256. No dependencies are installed by this package.
type OwnerPolicy struct {
	Installation       LocalInstallation `json:"installation"`
	EntrySHA256        string            `json:"entry_sha256"`
	PythonSHA256       string            `json:"python_sha256"`
	ConfigSHA256       string            `json:"config_sha256"`
	RuntimeFilesSHA256 map[string]string `json:"runtime_files_sha256"`
}

// LocalJob consumes a pre-existing scheduler/owner admission; it never creates,
// renews, authenticates, or reserves one. The caller owns the lease lifecycle.
type LocalJob struct {
	AdmissionPath       string
	PromptPath          string
	AttemptID           string
	ModelManifestSHA256 string
	MaxTokens           int
	// Zero selects the hard ceiling (300 seconds). Earlier context/admission
	// deadlines always win; negative or over-ceiling timeouts are rejected.
	Timeout     time.Duration
	StdoutBytes int
	StderrBytes int
}

type InferenceResult struct {
	SchemaVersion          int    `json:"schema_version"`
	Template               string `json:"template"`
	RuntimeVersion         string `json:"runtime_version"`
	AttemptID              string `json:"attempt_id"`
	ModelManifestSHA256    string `json:"model_manifest_sha256"`
	Text                   string `json:"text"`
	EstimatedRequiredBytes uint64 `json:"estimated_required_bytes"`
}

type localConfig struct {
	SchemaVersion           int    `json:"schema_version"`
	ModelDirectory          string `json:"model_directory"`
	ModelManifestSHA256     string `json:"model_manifest_sha256"`
	DependencyReceipt       string `json:"dependency_receipt"`
	DependencyReceiptSHA256 string `json:"dependency_receipt_sha256"`
}

type admission struct {
	SchemaVersion       int    `json:"schema_version"`
	AttemptID           string `json:"attempt_id"`
	ModelManifestSHA256 string `json:"model_manifest_sha256"`
	ReservedBytes       uint64 `json:"reserved_bytes"`
	AvailableBytes      uint64 `json:"available_bytes"`
	OwnerReserveBytes   uint64 `json:"owner_reserve_bytes"`
	ObservedAtUnix      int64  `json:"observed_at_unix"`
	ExpiresAtUnix       int64  `json:"expires_at_unix"`
}

var attemptPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func validDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}

func matchesDigest(data []byte, expected string) bool {
	sum := sha256.Sum256(data)
	return validDigest(expected) && hex.EncodeToString(sum[:]) == expected
}

// strictObject rejects duplicates, missing/unknown fields, nulls, trailing JSON,
// invalid UTF-8 and non-object payloads. All current protocol objects are flat.
func strictObject(data []byte, dst any, keys ...string) error {
	if !utf8.Valid(data) {
		return ErrInput
	}
	d := json.NewDecoder(bytes.NewReader(data))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return ErrInput
	}
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}
	seen := make(map[string]bool, len(keys))
	for d.More() {
		t, err = d.Token()
		k, ok := t.(string)
		if err != nil || !ok || !allowed[k] || seen[k] {
			return ErrInput
		}
		seen[k] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrInput
		}
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') || len(seen) != len(allowed) {
		return ErrInput
	}
	if _, err = d.Token(); err != io.EOF {
		return ErrInput
	}
	if json.Unmarshal(data, dst) != nil {
		return ErrInput
	}
	return nil
}

func verifyPolicy(ctx context.Context, p OwnerPolicy) (localConfig, error) {
	var config localConfig
	i := p.Installation
	if _, err := i.base(); err != nil || !validDigest(p.EntrySHA256) ||
		!validDigest(p.PythonSHA256) || !validDigest(p.ConfigSHA256) ||
		len(p.RuntimeFilesSHA256) < 2 || len(p.RuntimeFilesSHA256) > 256 {
		return config, ErrPolicy
	}
	// Python is pinned separately: the installed environment still requires the
	// Python adapter's reviewed receipt, which this wrapper cannot bypass.
	python, err := readLocalFile(i.Python, 128*1024*1024, false, true)
	if err != nil || !matchesDigest(python, p.PythonSHA256) {
		return config, ErrPolicy
	}
	root := filepath.Dir(i.Entry)
	if p.RuntimeFilesSHA256[filepath.Base(i.Entry)] != p.EntrySHA256 {
		return config, ErrPolicy
	}
	// These are the adapter's import closure, not an arbitrary entry/module API.
	for _, name := range []string{"__init__.py", "cli.py", "runtime.py", "security.py", "ring.py"} {
		if !validDigest(p.RuntimeFilesSHA256["nexal_mlx/"+name]) {
			return config, ErrPolicy
		}
	}
	total, count := 0, 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil || entry == nil || entry.Type()&os.ModeSymlink != 0 {
			return ErrPolicy
		}
		if entry.IsDir() {
			if err := checkLocalDirectory(path); err != nil {
				return ErrPolicy
			}
			// Only the adjacent adapter package is importable from the entry
			// directory. No sibling modules or unchecked bytecode caches.
			if path != root && path != filepath.Join(root, "nexal_mlx") {
				return ErrPolicy
			}
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return ErrPolicy
		}
		name = filepath.ToSlash(name)
		expected, ok := p.RuntimeFilesSHA256[name]
		if !ok || !validDigest(expected) || !strings.HasSuffix(name, ".py") ||
			(name != filepath.Base(i.Entry) && !strings.HasPrefix(name, "nexal_mlx/")) {
			return ErrPolicy
		}
		data, err := readLocalFile(path, maxTreeBytes-total, false, false)
		if err != nil || !matchesDigest(data, expected) {
			return ErrPolicy
		}
		total += len(data)
		count++
		return nil
	})
	if err != nil || count != len(p.RuntimeFilesSHA256) {
		return config, ErrPolicy
	}
	data, err := readLocalFile(i.Config, 64*1024, true, false)
	if err != nil || !matchesDigest(data, p.ConfigSHA256) {
		return config, ErrPolicy
	}
	if strictObject(data, &config, "schema_version", "model_directory", "model_manifest_sha256",
		"dependency_receipt", "dependency_receipt_sha256") != nil ||
		config.SchemaVersion != 1 || !validDigest(config.ModelManifestSHA256) ||
		!validDigest(config.DependencyReceiptSHA256) || !localPath(config.ModelDirectory) ||
		!localPath(config.DependencyReceipt) {
		return localConfig{}, ErrPolicy
	}
	return config, nil
}

func readAdmission(job LocalJob, model string) (admission, error) {
	var grant admission
	data, err := readLocalFile(job.AdmissionPath, 64*1024, true, false)
	if err != nil || strictObject(data, &grant, "schema_version", "attempt_id", "model_manifest_sha256",
		"reserved_bytes", "available_bytes", "owner_reserve_bytes", "observed_at_unix", "expires_at_unix") != nil {
		return grant, ErrAdmission
	}
	now := time.Now()
	if grant.SchemaVersion != 1 || grant.AttemptID != job.AttemptID ||
		grant.ModelManifestSHA256 != job.ModelManifestSHA256 || model != job.ModelManifestSHA256 ||
		grant.ObservedAtUnix <= 0 || grant.ExpiresAtUnix <= 0 ||
		now.Before(time.Unix(grant.ObservedAtUnix, 0)) ||
		now.Sub(time.Unix(grant.ObservedAtUnix, 0)) > 15*time.Second ||
		!now.Before(time.Unix(grant.ExpiresAtUnix, 0)) ||
		time.Unix(grant.ExpiresAtUnix, 0).Sub(now) > 300*time.Second ||
		grant.ReservedBytes == 0 || grant.AvailableBytes <= grant.OwnerReserveBytes ||
		grant.ReservedBytes > 1<<63-1 || grant.AvailableBytes > 1<<63-1 || grant.OwnerReserveBytes > 1<<63-1 {
		return grant, ErrAdmission
	}
	return grant, nil
}

// RunLocalInference executes ONLY the fixed InferenceCommand, on Apple silicon.
// There is deliberately no exported platform bypass or process injection hook.
// Linux tests use an unexported command factory and a compiled Go test helper.
func RunLocalInference(ctx context.Context, owner OwnerPolicy, job LocalJob) (InferenceResult, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return InferenceResult{}, ErrPlatform
	}
	return runLocalInference(ctx, owner, job, func(ctx context.Context, c Command) *exec.Cmd {
		return exec.CommandContext(ctx, c.Executable, c.Args...)
	})
}

type commandFactory func(context.Context, Command) *exec.Cmd

func runLocalInference(ctx context.Context, owner OwnerPolicy, job LocalJob, factory commandFactory) (InferenceResult, error) {
	var zero InferenceResult
	if ctx == nil {
		return zero, ErrInput
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	command, err := InferenceCommand(owner.Installation, job.AdmissionPath, job.PromptPath, job.MaxTokens)
	if err != nil || !attemptPattern.MatchString(job.AttemptID) || !validDigest(job.ModelManifestSHA256) ||
		job.Timeout < 0 || job.Timeout > time.Duration(command.TimeoutSeconds)*time.Second ||
		job.StdoutBytes < 0 || job.StdoutBytes > DefaultStdoutBytes ||
		job.StderrBytes < 0 || job.StderrBytes > DefaultStderrBytes {
		return zero, ErrInput
	}
	if job.Timeout == 0 {
		job.Timeout = time.Duration(command.TimeoutSeconds) * time.Second
	}
	ctx, cancelTimeout := context.WithTimeout(ctx, job.Timeout)
	defer cancelTimeout()
	// This is only a cooperative per-config exclusion lock, NOT a memory
	// reservation or a cross-config scheduler. Hold it until group cleanup.
	unlock, err := acquireConfigLock(owner.Installation.Config)
	if err != nil {
		return zero, err
	}
	defer unlock()
	config, err := verifyPolicy(ctx, owner)
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err != nil {
		return zero, err
	}
	grant, err := readAdmission(job, config.ModelManifestSHA256)
	if err != nil {
		return zero, err
	}
	prompt, err := readLocalFile(job.PromptPath, 16*1024, false, false)
	if err != nil || !utf8.Valid(prompt) || len(bytes.TrimSpace(prompt)) == 0 {
		return zero, ErrInput
	}
	ctx, cancelLease := context.WithDeadline(ctx, time.Unix(grant.ExpiresAtUnix, 0))
	defer cancelLease()
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if job.StdoutBytes == 0 {
		job.StdoutBytes = DefaultStdoutBytes
	}
	if job.StderrBytes == 0 {
		job.StderrBytes = DefaultStderrBytes
	}
	childCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	stdout := &boundedOutput{limit: job.StdoutBytes, retain: true, cancel: cancelProcess}
	stderr := &boundedOutput{limit: job.StderrBytes, cancel: cancelProcess}
	cmd := factory(childCtx, command)
	cmd.Dir = filepath.Dir(owner.Installation.Entry)
	// Do not inherit PYTHONPATH, DYLD_*, proxy settings, HF credentials,
	// MLXLM_USE_MODELSCOPE, or arbitrary application secrets.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8",
		"HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_DATASETS_OFFLINE=1",
		"HF_HUB_DISABLE_TELEMETRY=1", "DO_NOT_TRACK=1"}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if prepareProcess(cmd) != nil {
		return zero, ErrProcess
	}
	// Caps Wait even if a descendant inherits a pipe and the leader exits.
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	if cmd.Start() != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrProcess
	}
	err = cmd.Wait()
	// Also tear down lingering descendants after normal or failed leader exit.
	_ = killProcessGroup(cmd)
	data, exceeded := stdout.snapshot()
	_, stderrExceeded := stderr.snapshot()
	if exceeded || stderrExceeded {
		return zero, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err != nil {
		return zero, ErrProcess
	}
	var result InferenceResult
	if strictObject(data, &result, "schema_version", "template", "runtime_version", "attempt_id",
		"model_manifest_sha256", "text", "estimated_required_bytes") != nil ||
		result.SchemaVersion != 1 || result.Template != "mlx-local-text-v1" ||
		result.RuntimeVersion != "0.2.0" || result.AttemptID != job.AttemptID ||
		result.ModelManifestSHA256 != job.ModelManifestSHA256 ||
		result.EstimatedRequiredBytes == 0 || result.EstimatedRequiredBytes > grant.ReservedBytes ||
		result.EstimatedRequiredBytes > grant.AvailableBytes-grant.OwnerReserveBytes {
		return zero, ErrResult
	}
	return result, nil
}

// boundedOutput never keeps more than limit bytes. Stderr is counted/discarded,
// never returned or logged. Overflow cancels the supervised process immediately.
type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	n, limit int
	retain   bool
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.n
	take := len(p)
	if take > remaining {
		take = remaining
	}
	if b.retain {
		b.data = append(b.data, p[:take]...)
	}
	b.n += take
	if take != len(p) && !b.exceeded {
		b.exceeded = true
		b.cancel()
	}
	return len(p), nil
}

func (b *boundedOutput) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...), b.exceeded
}
