package inference

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// PinsSchemaVersion is the version of <config dir>/inference/pins.json.
const PinsSchemaVersion = 1

const maxPinsBytes = 4 << 20

// PinsFile is the pins overlay: facts READ from a Hugging Face repository at
// one immutable commit, merged over the embedded (unpinned) catalog. It holds
// no secrets and grants nothing by itself: installing still verifies every byte
// against it.
type PinsFile struct {
	SchemaVersion int            `json:"schemaVersion"`
	Pins          map[string]Pin `json:"pins"`
}

// Pin is one model's pin.
type Pin struct {
	HFRepo    string `json:"hfRepo"`
	Revision  string `json:"revision"`
	License   string `json:"license"`
	ModelType string `json:"modelType,omitempty"`
	// Arch holds the architecture facts read from the pinned config.json. When
	// present the memory estimate is recomputed from it.
	Arch     *ArchSpec `json:"arch,omitempty"`
	Files    []PinFile `json:"files"`
	PinnedAt string    `json:"pinnedAt,omitempty"`
}

// PinsPath is where the overlay lives.
func PinsPath(inferenceDir string) string { return filepath.Join(inferenceDir, "pins.json") }

// ParsePins decodes and validates overlay bytes: schema version, duplicate keys
// (at any depth) rejected, unknown fields rejected, every provided value
// well-formed. Completeness is NOT required here (an incomplete pin loads and
// is reported as blocked by PinProblems).
func ParsePins(data []byte) (*PinsFile, error) {
	var p PinsFile
	if err := DecodeStrict(data, &p); err != nil {
		return nil, fmt.Errorf("pins: %w", err)
	}
	if p.SchemaVersion != PinsSchemaVersion {
		return nil, fmt.Errorf("pins: unsupported schemaVersion %d", p.SchemaVersion)
	}
	if p.Pins == nil {
		p.Pins = map[string]Pin{}
	}
	for id, pin := range p.Pins {
		if !entryIDs.MatchString(id) {
			return nil, fmt.Errorf("pins: bad model id %q", id)
		}
		if pin.HFRepo != "" && !repoName.MatchString(pin.HFRepo) {
			return nil, fmt.Errorf("pins: %s: hfRepo must be owner/name", id)
		}
		if pin.Revision != "" && !hexRev.MatchString(pin.Revision) {
			return nil, fmt.Errorf("pins: %s: revision must be a 40-hex commit, not a ref", id)
		}
		if len(pin.License) > 200 {
			return nil, fmt.Errorf("pins: %s: license too long", id)
		}
		if err := checkPinFiles(pin.Files); err != nil {
			return nil, fmt.Errorf("pins: %s: %w", id, err)
		}
		if a := pin.Arch; a != nil {
			for _, n := range []int{a.Layers, a.KVHeads, a.HeadDim, a.HiddenSize, a.IntermediateSize, a.VocabSize} {
				if n <= 0 || n > 1_000_000 {
					return nil, fmt.Errorf("pins: %s: arch values must be positive", id)
				}
			}
		}
	}
	return &p, nil
}

// LoadPins reads the overlay. A missing file is not an error (nil, nil). The
// file must be a regular file (not a symlink) with no group/other access.
func LoadPins(path string) (*PinsFile, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pins: %w", err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("pins: pins.json must be a regular file with mode 0600")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pins: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPinsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pins: %w", err)
	}
	if len(data) > maxPinsBytes {
		return nil, errors.New("pins: pins.json is too large")
	}
	return ParsePins(data)
}

// MergePins applies the overlay over a catalog and recomputes installability.
// An overlay entry for an unknown model, or one whose modelType differs from
// the catalog architecture, is an error.
func MergePins(c CatalogFile, p *PinsFile) (CatalogFile, error) {
	if p == nil || len(p.Pins) == 0 {
		return c, nil
	}
	out := c
	out.Entries = append([]CatalogEntry(nil), c.Entries...)
	idx := map[string]int{}
	for i, e := range out.Entries {
		idx[e.ID] = i
	}
	ids := make([]string, 0, len(p.Pins))
	for id := range p.Pins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		pin := p.Pins[id]
		i, ok := idx[id]
		if !ok {
			return CatalogFile{}, fmt.Errorf("pins: %q is not a catalog model", id)
		}
		e := out.Entries[i]
		if pin.ModelType != "" && pin.ModelType != e.Architecture {
			return CatalogFile{}, fmt.Errorf("pins: %s: modelType %q differs from catalog architecture %q", id, pin.ModelType, e.Architecture)
		}
		e.Pinned = true
		if pin.HFRepo != "" {
			e.HFRepo = pin.HFRepo
		}
		e.Revision = pin.Revision
		e.License = pin.License
		e.Files = append([]PinFile(nil), pin.Files...)
		sort.Slice(e.Files, func(a, b int) bool { return e.Files[a].Name < e.Files[b].Name })
		if pin.Arch != nil {
			e.Arch = *pin.Arch
			e.ArchSource = "read from the pinned config.json at revision " + pin.Revision
			e.Estimate = EstimateRank(e.Arch, e.ParamsBillions, e.BitsPerWeight, 1)
		}
		e.installState()
		out.Entries[i] = e
	}
	if err := out.Validate(); err != nil {
		return CatalogFile{}, err
	}
	return out, nil
}

// LoadCatalogWithPins loads the embedded catalog and merges
// <inferenceDir>/pins.json over it.
func LoadCatalogWithPins(inferenceDir string) (CatalogFile, error) {
	c, err := LoadCatalog()
	if err != nil {
		return CatalogFile{}, err
	}
	p, err := LoadPins(PinsPath(inferenceDir))
	if err != nil {
		return CatalogFile{}, err
	}
	return MergePins(c, p)
}

// SavePin merges one pin into <inferenceDir>/pins.json atomically (mode 0600,
// directory 0700). Existing pins for other models are preserved; an unreadable
// or invalid existing file is an error, never overwritten silently.
func SavePin(inferenceDir, id string, pin Pin) (string, error) {
	path := PinsPath(inferenceDir)
	cur, err := LoadPins(path)
	if err != nil {
		return path, err
	}
	if cur == nil {
		cur = &PinsFile{SchemaVersion: PinsSchemaVersion, Pins: map[string]Pin{}}
	}
	cur.Pins[id] = pin
	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return path, err
	}
	data = append(data, '\n')
	if _, err := ParsePins(data); err != nil { // never write what we would refuse to read
		return path, err
	}
	return path, writeFileAtomic(path, data, 0o600)
}
