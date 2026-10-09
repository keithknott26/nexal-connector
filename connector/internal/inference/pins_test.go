package inference

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func okPinJSON(extra string) string {
	h := func(c string) string { return strings.Repeat(c, 64) }
	return `{"schemaVersion":1,"pins":{"qwen3-4b-q4":{"hfRepo":"` + testRepo + `","revision":"` + testRev + `","license":"apache-2.0",` + extra + `
"files":[{"name":"config.json","size":10,"sha256":"` + h("1") + `"},{"name":"tokenizer.json","size":10,"sha256":"` + h("2") + `"},
{"name":"tokenizer_config.json","size":10,"sha256":"` + h("3") + `"},{"name":"model.safetensors","size":10,"sha256":"` + h("4") + `"}]}}}`
}

func TestParsePinsStrictness(t *testing.T) {
	if _, err := ParsePins([]byte(okPinJSON(""))); err != nil {
		t.Fatalf("good pins rejected: %v", err)
	}
	h := strings.Repeat("a", 64)
	tests := []struct{ name, in string }{
		{"duplicate top-level key", `{"schemaVersion":1,"schemaVersion":1,"pins":{}}`},
		{"duplicate model id", `{"schemaVersion":1,"pins":{"qwen3-4b-q4":{"files":[]},"qwen3-4b-q4":{"files":[]}}}`},
		{"duplicate key inside a file", `{"schemaVersion":1,"pins":{"qwen3-4b-q4":{"files":[{"name":"config.json","name":"config.json","size":1,"sha256":"` + h + `"}]}}}`},
		{"unknown field", `{"schemaVersion":1,"pins":{},"extra":1}`},
		{"unknown pin field", `{"schemaVersion":1,"pins":{"qwen3-4b-q4":{"files":[],"url":"https://evil"}}}`},
		{"wrong schema", `{"schemaVersion":2,"pins":{}}`},
		{"missing schema", `{"pins":{}}`},
		{"trailing data", `{"schemaVersion":1,"pins":{}} {}`},
		{"garbage", `{`},
		{"branch revision", strings.Replace(okPinJSON(""), testRev, "main", 1)},
		{"short revision", strings.Replace(okPinJSON(""), testRev, testRev[:39], 1)},
		{"upper-case revision", strings.Replace(okPinJSON(""), testRev, strings.ToUpper(testRev), 1)},
		{"bad sha", strings.Replace(okPinJSON(""), strings.Repeat("1", 64), "xyz", 1)},
		{"zero size", strings.Replace(okPinJSON(""), `"size":10`, `"size":0`, 1)},
		{"traversal file name", strings.Replace(okPinJSON(""), "config.json", "../config.json", 1)},
		{"python file", strings.Replace(okPinJSON(""), "tokenizer.json", "modeling.py", 1)},
		{"duplicate file", strings.Replace(okPinJSON(""), "tokenizer_config.json", "tokenizer.json", 1)},
		{"bad repo", strings.Replace(okPinJSON(""), testRepo, "../x", 1)},
		{"zero arch", okPinJSON(`"arch":{"layers":0,"kvHeads":8,"headDim":128,"hiddenSize":1,"intermediateSize":1,"vocabSize":1},`)},
		{"bad model id", strings.Replace(okPinJSON(""), "qwen3-4b-q4", "Bad/ID", 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePins([]byte(tc.in)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestLoadPinsFileRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	if p, err := LoadPins(path); p != nil || err != nil {
		t.Errorf("missing file: %v %v", p, err)
	}
	if err := os.WriteFile(path, []byte(okPinJSON("")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPins(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Errorf("mode 0644 accepted: %v", err)
	}
	os.Chmod(path, 0o600)
	if p, err := LoadPins(path); err != nil || p == nil {
		t.Errorf("0600 rejected: %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPins(link); err == nil {
		t.Error("symlink accepted")
	}
	big := filepath.Join(dir, "big.json")
	os.WriteFile(big, []byte(`{"schemaVersion":1,"pins":{},"x":"`+strings.Repeat("a", maxPinsBytes)+`"}`), 0o600)
	if _, err := LoadPins(big); err == nil {
		t.Error("oversized pins accepted")
	}
}

func TestMergeInstallableGating(t *testing.T) {
	base := baseCatalog(t)
	h := func(c string) string { return strings.Repeat(c, 64) }
	f := func(name string) PinFile { return PinFile{Name: name, Size: 5, SHA256: h("9")} }
	full := []PinFile{f("config.json"), f("tokenizer.json"), f("tokenizer_config.json"), f("model.safetensors")}
	without := func(name string) []PinFile {
		var out []PinFile
		for _, x := range full {
			if x.Name != name {
				out = append(out, x)
			}
		}
		return out
	}
	tests := []struct {
		name        string
		pin         Pin
		installable bool
		reason      []string // substrings that must appear
	}{
		{"complete", Pin{HFRepo: testRepo, Revision: testRev, License: "mit", Files: full}, true, nil},
		{"complete sharded with index", Pin{HFRepo: testRepo, Revision: testRev, License: "mit", Files: append(without("model.safetensors"),
			f("model-00001-of-00002.safetensors"), f("model-00002-of-00002.safetensors"), f("model.safetensors.index.json"))}, true, nil},
		{"no revision", Pin{HFRepo: testRepo, License: "mit", Files: full}, false, []string{"revision (40-hex commit)"}},
		{"no license", Pin{HFRepo: testRepo, Revision: testRev, Files: full}, false, []string{"license"}},
		{"no files", Pin{HFRepo: testRepo, Revision: testRev, License: "mit"}, false, []string{"config.json", "tokenizer.json", "tokenizer_config.json", ".safetensors weights"}},
		{"no tokenizer.json", Pin{HFRepo: testRepo, Revision: testRev, License: "mit", Files: without("tokenizer.json")}, false, []string{"size and SHA-256 of tokenizer.json"}},
		{"no config.json", Pin{HFRepo: testRepo, Revision: testRev, License: "mit", Files: without("config.json")}, false, []string{"size and SHA-256 of config.json"}},
		{"no weights", Pin{HFRepo: testRepo, Revision: testRev, License: "mit", Files: without("model.safetensors")}, false, []string{".safetensors weights"}},
		{"sharded without index", Pin{HFRepo: testRepo, Revision: testRev, License: "mit", Files: append(without("model.safetensors"),
			f("model-00001-of-00002.safetensors"), f("model-00002-of-00002.safetensors"))}, false, []string{"model.safetensors.index.json"}},
		{"two weights without index", Pin{HFRepo: testRepo, Revision: testRev, License: "mit", Files: append(full, f("model-00001-of-00002.safetensors"))}, false, []string{"model.safetensors.index.json"}},
		{"revision omitted and license omitted", Pin{HFRepo: testRepo, Files: full}, false, []string{"revision (40-hex commit)", "license"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := MergePins(base, &PinsFile{SchemaVersion: 1, Pins: map[string]Pin{testID: tc.pin}})
			if err != nil {
				t.Fatal(err)
			}
			var e CatalogEntry
			for _, x := range c.Entries {
				if x.ID == testID {
					e = x
				}
			}
			if !e.Pinned || e.Installable != tc.installable {
				t.Fatalf("pinned=%v installable=%v reason=%q", e.Pinned, e.Installable, e.InstallBlockedReason)
			}
			if tc.installable && e.InstallBlockedReason != "" {
				t.Errorf("installable entry has reason %q", e.InstallBlockedReason)
			}
			for _, want := range tc.reason {
				if !strings.Contains(e.InstallBlockedReason, want) {
					t.Errorf("reason %q lacks %q", e.InstallBlockedReason, want)
				}
			}
			if !tc.installable && !strings.HasPrefix(e.InstallBlockedReason, "pin incomplete: missing ") {
				t.Errorf("reason = %q", e.InstallBlockedReason)
			}
			// Every other entry stays unpinned.
			for _, x := range c.Entries {
				if x.ID != testID && (x.Pinned || x.Installable) {
					t.Errorf("%s leaked a pin", x.ID)
				}
			}
		})
	}
	// The package-level catalog and base are not mutated by a merge.
	for _, e := range base.Entries {
		if e.Pinned || len(e.Files) != 0 {
			t.Errorf("merge mutated the base catalog: %s", e.ID)
		}
	}
}

func TestMergeRejects(t *testing.T) {
	base := baseCatalog(t)
	full := okPinJSON("")
	p, err := ParsePins([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	pin := p.Pins[testID]
	if _, err := MergePins(base, &PinsFile{SchemaVersion: 1, Pins: map[string]Pin{"nope-1b-q4": pin}}); err == nil {
		t.Error("unknown model accepted")
	}
	pin.ModelType = "phi3"
	if _, err := MergePins(base, &PinsFile{SchemaVersion: 1, Pins: map[string]Pin{testID: pin}}); err == nil {
		t.Error("architecture mismatch accepted")
	}
	if c, err := MergePins(base, nil); err != nil || len(c.Entries) != len(base.Entries) {
		t.Error("nil overlay must be a no-op")
	}
}

func TestMergeRecomputesEstimateFromPinnedArch(t *testing.T) {
	base := baseCatalog(t)
	p, _ := ParsePins([]byte(okPinJSON("")))
	pin := p.Pins[testID]
	arch := ArchSpec{Layers: 40, KVHeads: 8, HeadDim: 128, HiddenSize: 2560, IntermediateSize: 9728, VocabSize: 151936}
	pin.Arch = &arch
	c, err := MergePins(base, &PinsFile{SchemaVersion: 1, Pins: map[string]Pin{testID: pin}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Entries {
		if e.ID == testID {
			if e.Arch != arch || e.Estimate != EstimateRank(arch, e.ParamsBillions, e.BitsPerWeight, 1) || !strings.Contains(e.ArchSource, testRev) {
				t.Errorf("estimate not recomputed: %+v", e)
			}
		}
	}
}

func TestSavePinMergesAtomically(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inference")
	p, _ := ParsePins([]byte(okPinJSON("")))
	pin := p.Pins[testID]
	path, err := SavePin(dir, testID, pin)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", st.Mode().Perm())
	}
	other := pin
	other.Revision = strings.Repeat("b", 40)
	if _, err := SavePin(dir, "qwen3-8b-q4", other); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPins(path)
	if err != nil || len(got.Pins) != 2 || got.Pins[testID].Revision != testRev || got.Pins["qwen3-8b-q4"].Revision != other.Revision {
		t.Fatalf("merge lost data: %+v %v", got, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("stray temp files: %v", ents)
	}
	// An invalid existing file is never overwritten.
	os.WriteFile(path, []byte(`{"schemaVersion":1,"pins":{},"pins":{}}`), 0o600)
	if _, err := SavePin(dir, testID, pin); err == nil {
		t.Error("overwrote an invalid pins file")
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"pins":{},"pins"`) {
		t.Error("invalid file was modified")
	}
	// Re-saving the same pin is idempotent and deterministic.
	os.Remove(path)
	SavePin(dir, testID, pin)
	a, _ := os.ReadFile(path)
	SavePin(dir, testID, pin)
	b, _ := os.ReadFile(path)
	if string(a) != string(b) {
		t.Error("not deterministic")
	}
}

func TestLoadCatalogWithPins(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCatalogWithPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Entries {
		if e.Installable {
			t.Fatal("nothing may be installable without pins")
		}
	}
	p, _ := ParsePins([]byte(okPinJSON("")))
	if _, err := SavePin(dir, testID, p.Pins[testID]); err != nil {
		t.Fatal(err)
	}
	c, err = LoadCatalogWithPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range c.Entries {
		if e.Installable {
			n++
			if e.ID != testID || e.Revision != testRev || len(e.Files) != 4 {
				t.Errorf("bad entry %+v", e)
			}
		}
	}
	if n != 1 {
		t.Errorf("installable = %d", n)
	}
	os.WriteFile(PinsPath(dir), []byte(`{"schemaVersion":1,"pins":{"qwen3-4b-q4":{"revision":"main","files":[]}}}`), 0o600)
	if _, err := LoadCatalogWithPins(dir); err == nil {
		t.Error("a mutable ref in the overlay must fail the load")
	}
}
