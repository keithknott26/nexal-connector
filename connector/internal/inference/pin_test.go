package inference

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func newPinner(t *testing.T, f *fakeHF) (*Pinner, string) {
	dir := t.TempDir()
	return &Pinner{InferenceDir: dir, Catalog: baseCatalog(t), Client: f.client(),
		Now: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }}, dir
}

func TestPinSuccessWritesInstallableOverlay(t *testing.T) {
	f := newFakeHF(t)
	f.treeExtra = []map[string]any{
		{"type": "directory", "path": "onnx"},
		{"type": "file", "path": "README.md", "size": 5},
		{"type": "file", "path": "modeling_qwen3.py", "size": 5},
		{"type": "file", "path": "onnx/model.onnx", "size": 5},
	}
	p, dir := newPinner(t, f)
	var progressed bool
	p.Progress = func(string, int64, int64) { progressed = true }
	res, err := p.Pin(context.Background(), testID, testRev, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Wrote || !progressed || res.License != "apache-2.0" || res.HFRepo != testRepo || res.Revision != testRev || len(res.Files) != 4 {
		t.Fatalf("result = %+v", res)
	}
	if res.Arch != (ArchSpec{Layers: 36, KVHeads: 8, HeadDim: 128, HiddenSize: 2560, IntermediateSize: 9728, VocabSize: 151936}) {
		t.Errorf("arch = %+v", res.Arch)
	}
	for _, want := range []string{"README.md", "modeling_qwen3.py", "onnx", "onnx/model.onnx"} {
		found := false
		for _, g := range res.Ignored {
			found = found || g == want
		}
		if !found {
			t.Errorf("ignored lacks %s: %v", want, res.Ignored)
		}
	}
	for _, pf := range res.Files {
		if pf.SHA256 != shaHex(f.files[pf.Name]) || pf.Size != int64(len(f.files[pf.Name])) {
			t.Errorf("bad pin for %s", pf.Name)
		}
	}
	if len(res.Commit) == 0 || res.PinJSON == nil || res.PinJSON.PinnedAt != "2026-10-09T12:00:00Z" {
		t.Errorf("commit/pin = %+v", res)
	}
	st, err := os.Stat(res.OverlayPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("overlay: %v %v", st, err)
	}
	c, err := LoadCatalogWithPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Entries {
		if e.ID == testID && (!e.Installable || e.License != "apache-2.0" || !strings.Contains(e.ArchSource, testRev)) {
			t.Errorf("not installable after pin: %+v", e)
		}
	}
	// Every file request used the exact commit, never a ref.
	for _, r := range f.requests() {
		if strings.Contains(r.Path, "/main") || strings.Contains(r.Path, "refs") {
			t.Errorf("mutable ref requested: %s", r.Path)
		}
	}
}

func TestPinRefusesMutableRefsWithoutAnyRequest(t *testing.T) {
	f := newFakeHF(t)
	p, dir := newPinner(t, f)
	for _, rev := range []string{"main", "master", "refs/pr/3", "v1.0", testRev[:39], strings.ToUpper(testRev), testRev + "0", ""} {
		_, err := p.Pin(context.Background(), testID, rev, "")
		wantCode(t, err, "mutable_revision")
	}
	if len(f.requests()) != 0 {
		t.Error("network used for a mutable ref")
	}
	if _, err := os.Stat(PinsPath(dir)); err == nil {
		t.Error("overlay written")
	}
}

func TestPinRefusals(t *testing.T) {
	cfgWith := func(mut func(m map[string]any)) []byte {
		m := goodConfig()
		mut(m)
		return jsonBytes(m)
	}
	tests := []struct {
		name string
		prep func(f *fakeHF)
		code string
		repo string
		blk  string // expected first blocker key
	}{
		{"auto_map", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { m["auto_map"] = map[string]any{"AutoModel": "modeling.X"} }))
		}, "remote_code_metadata", "", "auto_map"},
		{"_name_or_path", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { m["_name_or_path"] = "Qwen/Qwen3-4B" }))
		}, "remote_code_metadata", "", "_name_or_path"},
		{"nested trust_remote_code", func(f *fakeHF) {
			f.setFile("tokenizer_config.json", jsonBytes(map[string]any{"tokenizer_class": "Qwen2Tokenizer", "x": []any{map[string]any{"trust_remote_code": true}}}))
		}, "remote_code_metadata", "", "x[0].trust_remote_code"},
		{"_file key", func(f *fakeHF) {
			f.setFile("tokenizer_config.json", jsonBytes(map[string]any{"tokenizer_class": "Qwen2Tokenizer", "vocab_file": "v.txt"}))
		}, "remote_code_metadata", "", "vocab_file"},
		{"_path key", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { m["cache_path"] = "/x" }))
		}, "remote_code_metadata", "", "cache_path"},
		{"duplicate keys in config", func(f *fakeHF) {
			f.setFile("config.json", []byte(strings.Replace(string(jsonBytes(goodConfig())), `"model_type":"qwen3"`, `"model_type":"qwen3","model_type":"qwen3"`, 1)))
		}, "config_invalid", "", ""},
		{"wrong model_type", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { m["model_type"] = "llama" }))
		}, "config_invalid", "", ""},
		{"wrong architectures", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { m["architectures"] = []any{"LlamaForCausalLM"} }))
		}, "config_invalid", "", ""},
		{"missing hidden_size", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { delete(m, "hidden_size") }))
		}, "config_invalid", "", ""},
		{"fractional layers", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { m["num_hidden_layers"] = 36.5 }))
		}, "config_invalid", "", ""},
		{"heads not divisible", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { m["num_key_value_heads"] = 5 }))
		}, "config_invalid", "", ""},
		{"no tie_word_embeddings", func(f *fakeHF) {
			f.setFile("config.json", cfgWith(func(m map[string]any) { delete(m, "tie_word_embeddings") }))
		}, "config_invalid", "", ""},
		{"config not an object", func(f *fakeHF) { f.setFile("config.json", []byte(`[1]`)) }, "config_invalid", "", ""},
		{"tokenizer class not approved", func(f *fakeHF) {
			f.setFile("tokenizer_config.json", jsonBytes(map[string]any{"tokenizer_class": "CustomTokenizer"}))
		}, "tokenizer_not_approved", "", ""},
		{"gated", func(f *fakeHF) { f.gated = true }, "repo_not_public", "", ""},
		{"no license", func(f *fakeHF) { f.license = "" }, "license_missing", "", ""},
		{"revision resolves elsewhere", func(f *fakeHF) { f.infoSHA = strings.Repeat("e", 40) }, "revision_mismatch", "", ""},
		{"no tokenizer.json", func(f *fakeHF) { delete(f.files, "tokenizer.json") }, "unsupported_files", "", ""},
		{"no weights", func(f *fakeHF) { delete(f.files, "model.safetensors") }, "unsupported_files", "", ""},
		{"sharded without index", func(f *fakeHF) {
			delete(f.files, "model.safetensors")
			f.files["model-00001-of-00002.safetensors"] = []byte("aaaa")
			f.files["model-00002-of-00002.safetensors"] = []byte("bbbb")
		}, "unsupported_files", "", ""},
		{"index references an unpinned shard", func(f *fakeHF) {
			delete(f.files, "model.safetensors")
			f.files["model-00001-of-00002.safetensors"] = []byte("aaaa")
			f.files["model-00002-of-00002.safetensors"] = []byte("bbbb")
			f.files[weightIndexName] = jsonBytes(map[string]any{"weight_map": map[string]any{"a": "model-00003-of-00003.safetensors"}})
		}, "index_mismatch", "", ""},
		{"lfs digest disagrees with the bytes", func(f *fakeHF) { f.lfsOverride["model.safetensors"] = strings.Repeat("0", 64) }, "lfs_mismatch", "", ""},
		{"traversal path in the listing", func(f *fakeHF) {
			f.treeExtra = []map[string]any{{"type": "file", "path": "../../evil.safetensors", "size": 5}}
		}, "bad_remote_path", "", ""},
		{"unknown repo", func(f *fakeHF) {}, "repo_not_found", "mlx-community/does-not-exist", ""},
		{"bad repo flag", func(f *fakeHF) {}, "invalid_arguments", "../x", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHF(t)
			tc.prep(f)
			p, dir := newPinner(t, f)
			res, err := p.Pin(context.Background(), testID, testRev, tc.repo)
			wantCode(t, err, tc.code)
			if res.Wrote {
				t.Error("result says written")
			}
			if _, serr := os.Stat(PinsPath(dir)); serr == nil {
				t.Error("overlay written despite the refusal")
			}
			if tc.blk != "" && (len(res.Blockers) == 0 || res.Blockers[0].Key != tc.blk) {
				t.Errorf("blockers = %+v, want first %q", res.Blockers, tc.blk)
			}
			if tc.code == "remote_code_metadata" && len(res.Commit) == 0 {
				t.Error("no guidance printed for the blocker")
			}
		})
	}
}

func TestPinRepoOverrideAndModelChecks(t *testing.T) {
	f := newFakeHF(t)
	p, _ := newPinner(t, f)
	_, err := p.Pin(context.Background(), "nope-1b-q4", testRev, "")
	wantCode(t, err, "model_not_found")
	res, err := p.Pin(context.Background(), testID, testRev, testRepo)
	if err != nil || res.HFRepo != testRepo {
		t.Fatalf("explicit --repo: %v", err)
	}
}

func TestPinMergesWithExistingOverlay(t *testing.T) {
	f := newFakeHF(t)
	p, dir := newPinner(t, f)
	other, _ := ParsePins([]byte(okPinJSON("")))
	pin := other.Pins[testID]
	if _, err := SavePin(dir, "qwen3-8b-q4", pin); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pin(context.Background(), testID, testRev, ""); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPins(PinsPath(dir))
	if err != nil || len(got.Pins) != 2 {
		t.Fatalf("pins = %+v %v", got, err)
	}
}

func TestPinShardedHappyPath(t *testing.T) {
	f := newFakeHF(t)
	delete(f.files, "model.safetensors")
	f.files["model-00001-of-00002.safetensors"] = []byte("aaaaaaaa")
	f.files["model-00002-of-00002.safetensors"] = []byte("bbbbbbbb")
	f.files[weightIndexName] = jsonBytes(map[string]any{"weight_map": map[string]any{"a": "model-00001-of-00002.safetensors", "b": "model-00002-of-00002.safetensors"}})
	p, dir := newPinner(t, f)
	if _, err := p.Pin(context.Background(), testID, testRev, ""); err != nil {
		t.Fatal(err)
	}
	c, _ := LoadCatalogWithPins(dir)
	for _, e := range c.Entries {
		if e.ID == testID && (!e.Installable || len(e.Files) != 6) {
			t.Errorf("sharded pin not installable: %q", e.InstallBlockedReason)
		}
	}
}
