package inference

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// StateInstalledUnmeasured is the only state an install can produce: the files
// are exactly the pinned bytes, but the manifest's memory terms are catalog
// ESTIMATES and the runtime requires measured values, so nothing here is
// runnable until the owner finishes the runtime release gates.
const StateInstalledUnmeasured = "installed_unmeasured"

// StateDamaged is reported by status/verify when files no longer match.
const StateDamaged = "damaged"

// ManifestFileName is the file the MLX runtime reads (runtimes/nexal_mlx/security.py).
const ManifestFileName = "nexal-model-manifest.json"

// ManifestMemory is the runtime's memory block (exactly these six keys).
type ManifestMemory struct {
	WeightsBytes    int64 `json:"weights_bytes"`
	KVBytesPerToken int64 `json:"kv_bytes_per_token"`
	ActivationBytes int64 `json:"activation_bytes"`
	BufferBytes     int64 `json:"buffer_bytes"`
	LoadPeakBytes   int64 `json:"load_peak_bytes"`
	SafetyBytes     int64 `json:"safety_bytes"`
}

// Manifest is runtimes schema 1, exactly.
type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	ModelID       string            `json:"model_id"`
	Revision      string            `json:"revision"`
	License       string            `json:"license"`
	ModelType     string            `json:"model_type"`
	Files         map[string]string `json:"files"`
	Memory        ManifestMemory    `json:"memory"`
}

// BuildManifest maps a pinned catalog entry to the runtime manifest. Memory
// terms come from the catalog ESTIMATE, adjusted only upward where the runtime
// would otherwise reject them (weights_bytes may not understate the stored
// weights). Warnings say where the estimate was exceeded by the real files.
func BuildManifest(e CatalogEntry) (Manifest, []string, error) {
	if !e.Installable {
		return Manifest{}, nil, fmt.Errorf("%s is not installable: %s", e.ID, e.InstallBlockedReason)
	}
	var warns []string
	files := make(map[string]string, len(e.Files))
	var weightFiles int64
	for _, f := range e.Files {
		files[f.Name] = f.SHA256
		if IsWeightFile(f.Name) {
			weightFiles += f.Size
		}
	}
	est := e.Estimate
	weights := est.WeightsBytes
	if weightFiles > weights {
		warns = append(warns, fmt.Sprintf("The weight files (%d bytes) are larger than the catalog estimate (%d bytes); the manifest uses the file size.", weightFiles, est.WeightsBytes))
		weights = weightFiles
	}
	kv := (est.KVBytes + ContextTokens - 1) / ContextTokens
	mem := ManifestMemory{
		WeightsBytes: weights, KVBytesPerToken: kv, ActivationBytes: est.ActivationsBytes,
		BufferBytes: est.TemporaryBytes, LoadPeakBytes: weights + est.TemporaryBytes, SafetyBytes: est.SafetyBytes,
	}
	for name, v := range map[string]int64{"weights_bytes": mem.WeightsBytes, "kv_bytes_per_token": mem.KVBytesPerToken,
		"activation_bytes": mem.ActivationBytes, "buffer_bytes": mem.BufferBytes, "load_peak_bytes": mem.LoadPeakBytes, "safety_bytes": mem.SafetyBytes} {
		if v < 1 {
			return Manifest{}, nil, fmt.Errorf("memory term %s is not positive", name)
		}
	}
	return Manifest{SchemaVersion: 1, ModelID: e.ID, Revision: e.Revision, License: e.License,
		ModelType: e.Architecture, Files: files, Memory: mem}, warns, nil
}

// Bytes is the canonical manifest encoding (sorted file keys, trailing newline).
func (m Manifest) Bytes() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func sortedNames(files []PinFile) []string {
	n := make([]string, 0, len(files))
	for _, f := range files {
		n = append(n, f.Name)
	}
	sort.Strings(n)
	return n
}
