package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Pinner implements `nexal inference pin`: the maintainer/owner step that runs
// on a Mac WITH network access. It reads one exact Hugging Face commit, hashes
// every allowlisted file, reads the license and config.json, and merges the
// result into the pins overlay. It writes nothing under models/.
type Pinner struct {
	InferenceDir string
	Catalog      CatalogFile // base catalog (used for architecture and default repo)
	Client       *HFClient
	Now          func() time.Time
	// Progress is called while a file is hashed (optional).
	Progress func(file string, bytes, total int64)
}

// PinBlocker is a reason a pin was refused, quoted from the repository.
type PinBlocker struct {
	File   string `json:"file"`
	Key    string `json:"key"`
	Detail string `json:"detail"`
}

// PinResult is `nexal inference pin --json`.
type PinResult struct {
	SchemaVersion int          `json:"schemaVersion"`
	ModelID       string       `json:"modelId"`
	HFRepo        string       `json:"hfRepo"`
	Revision      string       `json:"revision"`
	License       string       `json:"license"`
	ModelType     string       `json:"modelType"`
	Arch          ArchSpec     `json:"arch"`
	Files         []PinFile    `json:"files"`
	Ignored       []string     `json:"ignored"`
	TotalBytes    int64        `json:"totalBytes"`
	Blockers      []PinBlocker `json:"blockers"`
	OverlayPath   string       `json:"overlayPath"`
	Wrote         bool         `json:"wrote"`
	PinJSON       *Pin         `json:"pin,omitempty"`
	Commit        []string     `json:"commit"`
}

var tokenizerClasses = map[string][]string{
	"qwen3": {"PreTrainedTokenizerFast", "Qwen2Tokenizer", "Qwen2TokenizerFast"},
	"phi3":  {"PreTrainedTokenizerFast", "GPT2Tokenizer", "GPT2TokenizerFast"},
}

var modelClasses = map[string]string{"qwen3": "Qwen3ForCausalLM", "phi3": "Phi3ForCausalLM"}

// remoteCodeKeys mirrors runtimes/nexal_mlx/security.py check_metadata.
func remoteCodeBlockers(file string, v any, path string, out *[]PinBlocker) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			switch {
			case k == "auto_map" || k == "model_file" || k == "trust_remote_code" || k == "_name_or_path":
				*out = append(*out, PinBlocker{File: file, Key: p, Detail: "custom model code or remote metadata; the runtime refuses it and nexal does not sanitize files"})
			case strings.HasSuffix(k, "_file") || strings.HasSuffix(k, "_path"):
				*out = append(*out, PinBlocker{File: file, Key: p, Detail: "metadata that could redirect local file loading; the runtime refuses it"})
			}
			remoteCodeBlockers(file, t[k], p, out)
		}
	case []any:
		for i, it := range t {
			remoteCodeBlockers(file, it, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

func intField(m map[string]any, k string) (int, bool) {
	n, ok := m[k].(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil || i < 1 || i > 1_000_000 {
		return 0, false
	}
	return int(i), true
}

// archFromConfig reads the architecture facts the memory estimate needs.
func archFromConfig(modelType string, cfg map[string]any) (ArchSpec, error) {
	if mt, _ := cfg["model_type"].(string); mt != modelType {
		return ArchSpec{}, errf("config_invalid", "", "config.json model_type %q does not match the catalog architecture %q", mt, modelType)
	}
	if a, ok := cfg["architectures"]; ok {
		list, _ := a.([]any)
		if len(list) != 1 || list[0] != modelClasses[modelType] {
			return ArchSpec{}, errf("config_invalid", "", "config.json architectures is not [%q]", modelClasses[modelType])
		}
	}
	var a ArchSpec
	var ok [5]bool
	a.HiddenSize, ok[0] = intField(cfg, "hidden_size")
	a.Layers, ok[1] = intField(cfg, "num_hidden_layers")
	a.IntermediateSize, ok[2] = intField(cfg, "intermediate_size")
	a.VocabSize, ok[3] = intField(cfg, "vocab_size")
	heads, hok := intField(cfg, "num_attention_heads")
	a.KVHeads, ok[4] = intField(cfg, "num_key_value_heads")
	for _, o := range ok {
		if !o {
			return ArchSpec{}, errf("config_invalid", "", "config.json lacks a required positive integer field")
		}
	}
	if !hok || heads%a.KVHeads != 0 {
		return ArchSpec{}, errf("config_invalid", "", "config.json has invalid attention head counts")
	}
	if hd, has := intField(cfg, "head_dim"); has {
		a.HeadDim = hd
	} else if modelType == "qwen3" {
		return ArchSpec{}, errf("config_invalid", "", "config.json lacks head_dim (required for qwen3)")
	} else {
		a.HeadDim = a.HiddenSize / heads
	}
	if _, isBool := cfg["tie_word_embeddings"].(bool); !isBool {
		return ArchSpec{}, errf("config_invalid", "", "config.json lacks an explicit tie_word_embeddings")
	}
	return a, nil
}

// Pin runs the pin flow. On a remote_code_metadata refusal the returned result
// carries the blockers and nothing is written.
func (p *Pinner) Pin(ctx context.Context, modelID, revision, repoOverride string) (PinResult, error) {
	res := PinResult{SchemaVersion: 1, ModelID: modelID, Files: []PinFile{}, Ignored: []string{}, Blockers: []PinBlocker{}, Commit: []string{}}
	if !hexRev.MatchString(revision) {
		return res, errf("mutable_revision", "pass the full 40-hex commit id of the reviewed snapshot (branch names and tags can move)", "refusing %q: a pin must name an immutable 40-hex commit, not a branch or tag", revision)
	}
	res.Revision = revision
	var entry *CatalogEntry
	for i := range p.Catalog.Entries {
		if p.Catalog.Entries[i].ID == modelID {
			entry = &p.Catalog.Entries[i]
		}
	}
	if entry == nil {
		return res, errf("model_not_found", "run `nexal inference preflight --json` to list model ids", "%q is not a catalog model", modelID)
	}
	repo := repoOverride
	if repo == "" {
		repo = entry.HFRepo
	}
	if !repoName.MatchString(repo) {
		return res, errf("invalid_arguments", "use --repo owner/name", "invalid Hugging Face repository %q", repo)
	}
	res.HFRepo, res.ModelType = repo, entry.Architecture
	res.OverlayPath = PinsPath(p.InferenceDir)

	info, err := p.Client.Info(ctx, repo, revision)
	if err != nil {
		return res, err
	}
	if info.SHA != revision {
		return res, errf("revision_mismatch", "", "Hugging Face resolved the revision to %q, not %q", info.SHA, revision)
	}
	if info.Gated {
		return res, errf("repo_not_public", "only public, ungated repositories can be pinned; no credentials are sent", "the repository is gated or private")
	}
	if info.License == "" {
		return res, errf("license_missing", "the repository must declare a license in its model card; review it by hand before pinning", "the repository declares no license")
	}
	res.License = info.License

	tree, err := p.Client.Tree(ctx, repo, revision)
	if err != nil {
		return res, err
	}
	type want struct {
		PinFile
		lfsSHA string
	}
	var picks []want
	for _, t := range tree {
		if strings.ContainsAny(t.Path, "\\\x00") || strings.HasPrefix(t.Path, "/") || strings.Contains(t.Path, "..") {
			return res, errf("bad_remote_path", "do not pin this repository", "the repository lists a suspicious path %q", t.Path)
		}
		if t.Type != "file" || strings.Contains(t.Path, "/") || !AllowedModelFile(t.Path) {
			res.Ignored = append(res.Ignored, t.Path)
			continue
		}
		size, lfs := t.Size, ""
		if t.LFS != nil {
			size, lfs = t.LFS.Size, t.LFS.OID
			if !hex64.MatchString(lfs) {
				return res, errf("api_invalid", "", "%s: the listing carries an invalid LFS digest", t.Path)
			}
		}
		if size <= 0 || size > maxPinnedFileBytes {
			return res, errf("api_invalid", "", "%s has an unusable size in the listing", t.Path)
		}
		picks = append(picks, want{PinFile{Name: t.Path, Size: size}, lfs})
	}
	sort.Strings(res.Ignored)
	sort.Slice(picks, func(i, j int) bool { return picks[i].Name < picks[j].Name })

	have := map[string]bool{}
	weights, sharded := 0, false
	for _, w := range picks {
		have[w.Name] = true
		if IsWeightFile(w.Name) {
			weights++
			if w.Name != "model.safetensors" {
				sharded = true
			}
		}
	}
	var missing []string
	for _, n := range []string{"config.json", "tokenizer.json", "tokenizer_config.json"} {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if weights == 0 {
		missing = append(missing, "*.safetensors weights")
	}
	if (sharded || weights > 1) && !have[weightIndexName] {
		missing = append(missing, weightIndexName)
	}
	if len(missing) > 0 {
		return res, errf("unsupported_files", "choose a repository that ships safetensors weights and fast tokenizer files", "the repository is missing %s", strings.Join(missing, ", "))
	}

	var total int64
	for _, w := range picks {
		total += w.Size
	}
	res.TotalBytes = total

	contents := map[string][]byte{}
	for i := range picks {
		w := &picks[i]
		keep := strings.HasSuffix(w.Name, ".json") && w.Name != "tokenizer.json"
		name, size := w.Name, w.Size
		sum, body, err := p.Client.StreamHash(ctx, repo, revision, name, size, keep, func(n int64) {
			if p.Progress != nil {
				p.Progress(name, n, size)
			}
		})
		if err != nil {
			return res, err
		}
		if w.lfsSHA != "" && w.lfsSHA != sum {
			return res, errf("lfs_mismatch", "do not pin this snapshot", "%s does not match the digest Hugging Face lists for it", name)
		}
		w.SHA256 = sum
		if keep {
			contents[name] = body
		}
		res.Files = append(res.Files, w.PinFile)
	}

	// Inspect small metadata files exactly as the runtime will.
	parsed := map[string]map[string]any{}
	for _, name := range sortedKeys(contents) {
		if err := CheckNoDuplicateKeys(contents[name]); err != nil {
			return res, errf("config_invalid", "", "%s is not strict JSON: %v", name, err)
		}
		dec := json.NewDecoder(bytes.NewReader(contents[name]))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil || m == nil {
			return res, errf("config_invalid", "", "%s is not a JSON object", name)
		}
		parsed[name] = m
		remoteCodeBlockers(name, m, "", &res.Blockers)
	}
	if len(res.Blockers) > 0 {
		res.Commit = []string{"Nothing was written. This snapshot carries metadata the MLX runtime refuses; nexal does not edit model files.",
			"Choose a snapshot without it, or have the owner sanitize and re-host files under review (runtimes/README.md step 3) and pin that."}
		return res, errf("remote_code_metadata", "pick another snapshot, or review and sanitize the files under owner control before pinning", "the snapshot carries %d remote-code or file-redirect metadata key(s), first: %s in %s", len(res.Blockers), res.Blockers[0].Key, res.Blockers[0].File)
	}
	arch, err := archFromConfig(entry.Architecture, parsed["config.json"])
	if err != nil {
		return res, err
	}
	res.Arch = arch
	if tc, _ := parsed["tokenizer_config.json"]["tokenizer_class"].(string); !containsStr(tokenizerClasses[entry.Architecture], tc) {
		return res, errf("tokenizer_not_approved", "", "tokenizer_class %q is not approved for %s", tc, entry.Architecture)
	}
	if idx, ok := parsed[weightIndexName]; ok {
		wm, _ := idx["weight_map"].(map[string]any)
		if len(wm) == 0 {
			return res, errf("index_mismatch", "", "%s has no weight_map", weightIndexName)
		}
		for _, v := range wm {
			target, _ := v.(string)
			if !IsWeightFile(target) || !have[target] {
				return res, errf("index_mismatch", "", "%s references %q, which is not a pinned weight file", weightIndexName, target)
			}
		}
	}

	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	pin := Pin{HFRepo: repo, Revision: revision, License: info.License, ModelType: entry.Architecture, Arch: &arch,
		Files: res.Files, PinnedAt: now.UTC().Format(time.RFC3339)}
	path, err := SavePin(p.InferenceDir, modelID, pin)
	if err != nil {
		return res, err
	}
	res.OverlayPath, res.Wrote, res.PinJSON = path, true, &pin
	res.Commit = []string{
		"Pin written to " + path + " (mode 0600).",
		"To share this pin, commit the \"pin\" entry (or the pins.json file) in your reviewed pin set; a teammate drops it at <config dir>/inference/pins.json. Review the revision and every hash against the Hugging Face commit page first.",
		"Then install on the target Mac: nexal inference install " + modelID,
	}
	return res, nil
}

func sortedKeys(m map[string][]byte) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
