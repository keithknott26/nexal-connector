package inference

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"nexal/connector/internal/pool"
)

//go:embed catalog.json
var catalogJSON []byte

// CatalogFile is the embedded, curated catalog. It is DATA the owner can audit:
// nothing in it is downloaded, and an entry cannot be installed until it is
// pinned (revision and SHA-256 filled in by a reviewed change).
type CatalogFile struct {
	SchemaVersion int `json:"schemaVersion"`
	// Runtime is the runtime profile the entries were sized for.
	Runtime       string         `json:"runtime"`
	MLXVersion    string         `json:"mlxVersion"`
	MLXLMVersion  string         `json:"mlxLmVersion"`
	MaxInputTok   int            `json:"runtimeMaxInputTokens"`
	MaxOutputTok  int            `json:"runtimeMaxOutputTokens"`
	EstimateLabel string         `json:"estimateLabel"`
	Formula       string         `json:"formula"`
	Entries       []CatalogEntry `json:"entries"`
}

// CatalogEntry is one model + quantization.
type CatalogEntry struct {
	ID          string `json:"id"`
	Family      string `json:"family"`
	DisplayName string `json:"displayName"`
	// Architecture is the runtime model_type (runtimes/nexal_mlx/profiles.py).
	Architecture   string   `json:"architecture"`
	ParamsBillions float64  `json:"paramsBillions"`
	Quantization   string   `json:"quantization"`
	BitsPerWeight  float64  `json:"bitsPerWeight"`
	Arch           ArchSpec `json:"arch"`
	// ArchSource says where Arch came from. It was NOT read from a pinned
	// config.json, so it must be re-checked when the entry is pinned.
	ArchSource string `json:"archSource"`
	// Estimate is the single-host planner input (pool.RankMemory). ESTIMATE.
	Estimate pool.RankMemory `json:"estimate"`
	// HFRepo is the Hugging Face repository the files are pinned from
	// ("owner/name"). It is a name, never a URL; URLs are built only by hf.go.
	HFRepo string `json:"hfRepo"`
	// License: in the embedded catalog this is the license the model card was
	// recalled to carry. A pinned entry carries the license READ from the repo
	// at the pinned revision (empty if the pin has none, which blocks install).
	License                 string `json:"license"`
	AdvertisedContextTokens int    `json:"advertisedContextTokens"`
	RuntimeMaxInputTokens   int    `json:"runtimeMaxInputTokens"`
	RuntimeMaxOutputTokens  int    `json:"runtimeMaxOutputTokens"`
	// Pinning. All empty/false in the embedded catalog. A pins overlay
	// (pins.go) fills Revision and Files with values read from the repo at one
	// immutable commit. Installable is computed, never trusted from input:
	// see PinProblems.
	Pinned               bool      `json:"pinned"`
	Revision             string    `json:"revision"`
	Files                []PinFile `json:"files"`
	Installable          bool      `json:"installable"`
	InstallBlockedReason string    `json:"installBlockedReason"`
}

// PinFile is one pinned file of a model snapshot.
type PinFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var (
	hex64    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexRev   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	entryIDs = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
	repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
)

// Manifest-visible file names, mirroring runtimes/nexal_mlx/security.py
// (MODEL_FILES and WEIGHT). Only these may ever be downloaded or declared.
var (
	weightName    = regexp.MustCompile(`^model(?:-\d{5}-of-\d{5})?\.safetensors$`)
	allowedSimple = map[string]bool{
		"config.json": true, "generation_config.json": true, "tokenizer.json": true,
		"tokenizer_config.json": true, "special_tokens_map.json": true,
		"model.safetensors.index.json": true, "added_tokens.json": true,
	}
)

const (
	weightIndexName = "model.safetensors.index.json"
	// maxPinnedFileBytes bounds any single pinned file (a sanity cap, not a
	// claim about real models).
	maxPinnedFileBytes = int64(256) << 30
)

// AllowedModelFile reports whether name may be downloaded into a model
// directory: a plain file name on the runtime allowlist.
func AllowedModelFile(name string) bool {
	return allowedSimple[name] || weightName.MatchString(name)
}

// IsWeightFile reports whether name is a safetensors weight file.
func IsWeightFile(name string) bool { return weightName.MatchString(name) }

// checkPinFiles validates formats only (not completeness).
func checkPinFiles(files []PinFile) error {
	seen := map[string]bool{}
	for _, f := range files {
		if !AllowedModelFile(f.Name) {
			return fmt.Errorf("file name %q is not on the runtime allowlist", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("duplicate file %q", f.Name)
		}
		seen[f.Name] = true
		if f.Size <= 0 || f.Size > maxPinnedFileBytes {
			return fmt.Errorf("file %q has an invalid size", f.Name)
		}
		if !hex64.MatchString(f.SHA256) {
			return fmt.Errorf("file %q has an invalid sha256", f.Name)
		}
	}
	return nil
}

// PinProblems lists exactly what is missing for e to be installable. Empty
// means installable. Order is stable.
func (e CatalogEntry) PinProblems() []string {
	var p []string
	if !e.Pinned {
		return []string{"revision (40-hex commit)", "size and SHA-256 of config.json, tokenizer.json, tokenizer_config.json and the .safetensors weights"}
	}
	if !hexRev.MatchString(e.Revision) {
		p = append(p, "revision (40-hex commit)")
	}
	if !repoName.MatchString(e.HFRepo) {
		p = append(p, "hfRepo")
	}
	if e.License == "" {
		p = append(p, "license")
	}
	have := map[string]PinFile{}
	for _, f := range e.Files {
		have[f.Name] = f
	}
	ok := func(name string) bool {
		f, found := have[name]
		return found && f.Size > 0 && hex64.MatchString(f.SHA256)
	}
	for _, need := range []string{"config.json", "tokenizer.json", "tokenizer_config.json"} {
		if !ok(need) {
			p = append(p, "size and SHA-256 of "+need)
		}
	}
	weights, sharded := 0, false
	for _, f := range e.Files {
		if IsWeightFile(f.Name) {
			if ok(f.Name) {
				weights++
			}
			if f.Name != "model.safetensors" {
				sharded = true
			}
		}
	}
	if weights == 0 {
		p = append(p, "size and SHA-256 of the .safetensors weights")
	}
	if (sharded || weights > 1) && !ok(weightIndexName) {
		p = append(p, "size and SHA-256 of "+weightIndexName+" (sharded weights)")
	}
	if err := checkPinFiles(e.Files); err != nil {
		p = append(p, "valid file list ("+err.Error()+")")
	}
	return p
}

// installState derives Installable and InstallBlockedReason from the pin.
func (e *CatalogEntry) installState() {
	probs := e.PinProblems()
	e.Installable = len(probs) == 0
	switch {
	case e.Installable:
		e.InstallBlockedReason = ""
	case !e.Pinned:
		e.InstallBlockedReason = "not pinned: missing " + strings.Join(probs, "; missing ") + " (run `nexal inference pin " + e.ID + " --revision <40-hex-commit>` on a Mac with network access)"
	default:
		e.InstallBlockedReason = "pin incomplete: missing " + strings.Join(probs, "; missing ")
	}
}

// Validate rejects a catalog that could mislead: duplicate ids, estimates that
// do not match the documented formula, an entry claiming to be pinned or
// installable without a real 40-hex revision and 64-hex SHA-256, or a pinned
// field filled in on an entry that says it is not pinned.
func (c CatalogFile) Validate() error {
	if c.SchemaVersion != 1 || len(c.Entries) == 0 {
		return errors.New("catalog: unsupported schema or empty")
	}
	if c.MaxInputTok != RuntimeMaxInputTokens || c.MaxOutputTok != RuntimeMaxOutputTokens {
		return errors.New("catalog: runtime token limits differ from the runtime profile")
	}
	seen := map[string]bool{}
	for _, e := range c.Entries {
		if !entryIDs.MatchString(e.ID) || seen[e.ID] {
			return fmt.Errorf("catalog: bad or duplicate id %q", e.ID)
		}
		seen[e.ID] = true
		if e.Architecture != "qwen3" && e.Architecture != "phi3" {
			return fmt.Errorf("catalog: %s: architecture is not in the runtime allowlist", e.ID)
		}
		want := EstimateRank(e.Arch, e.ParamsBillions, e.BitsPerWeight, 1)
		if e.Estimate != want {
			return fmt.Errorf("catalog: %s: estimate does not match the documented formula", e.ID)
		}
		if _, err := e.Estimate.Total(); err != nil {
			return fmt.Errorf("catalog: %s: %w", e.ID, err)
		}
		if e.RuntimeMaxInputTokens != RuntimeMaxInputTokens || e.RuntimeMaxOutputTokens != RuntimeMaxOutputTokens {
			return fmt.Errorf("catalog: %s: context limits differ from the runtime", e.ID)
		}
		if !repoName.MatchString(e.HFRepo) {
			return fmt.Errorf("catalog: %s: hfRepo must be owner/name", e.ID)
		}
		if e.Pinned {
			if e.Revision != "" && !hexRev.MatchString(e.Revision) {
				return fmt.Errorf("catalog: %s: a revision must be a 40-hex commit, not a ref", e.ID)
			}
			if err := checkPinFiles(e.Files); err != nil {
				return fmt.Errorf("catalog: %s: %w", e.ID, err)
			}
		} else if e.Revision != "" || len(e.Files) != 0 || e.Installable {
			return fmt.Errorf("catalog: %s: unpinned entries must have no revision or files and installable=false", e.ID)
		}
		if want := e.Pinned && len(e.PinProblems()) == 0; e.Installable != want {
			return fmt.Errorf("catalog: %s: installable=%v disagrees with the pin (%v)", e.ID, e.Installable, e.PinProblems())
		}
		if !e.Installable && e.InstallBlockedReason == "" {
			return fmt.Errorf("catalog: %s: non-installable entries must say why", e.ID)
		}
	}
	return nil
}

// LoadCatalog parses and validates the embedded catalog, ordered largest model
// first (parameters, then more bits per weight).
func LoadCatalog() (CatalogFile, error) {
	return ParseCatalog(catalogJSON)
}

// ParseCatalog is LoadCatalog for supplied bytes (tests).
func ParseCatalog(data []byte) (CatalogFile, error) {
	var c CatalogFile
	if err := json.Unmarshal(data, &c); err != nil {
		return CatalogFile{}, fmt.Errorf("catalog: %w", err)
	}
	if err := c.Validate(); err != nil {
		return CatalogFile{}, err
	}
	sort.SliceStable(c.Entries, func(i, j int) bool {
		a, b := c.Entries[i], c.Entries[j]
		if a.ParamsBillions != b.ParamsBillions {
			return a.ParamsBillions > b.ParamsBillions
		}
		return a.BitsPerWeight > b.BitsPerWeight
	})
	// The blocked reason is computed so it states exactly what is missing.
	for i := range c.Entries {
		c.Entries[i].installState()
	}
	return c, nil
}
