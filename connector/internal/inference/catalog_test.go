package inference

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogLoadsAndIsHonest(t *testing.T) {
	c, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if c.MLXVersion != "0.29.3" || c.MLXLMVersion != "0.28.4" {
		t.Errorf("versions %s/%s", c.MLXVersion, c.MLXLMVersion)
	}
	families := map[string]int{}
	sizes := map[float64]bool{}
	for i, e := range c.Entries {
		families[e.Family]++
		sizes[e.ParamsBillions] = true
		if e.Pinned || e.Revision != "" || len(e.Files) != 0 || e.Installable {
			t.Errorf("%s must be unpinned and not installable: %+v", e.ID, e)
		}
		if !strings.HasPrefix(e.InstallBlockedReason, "not pinned: missing revision (40-hex commit); missing size and SHA-256 of config.json") ||
			!strings.Contains(e.InstallBlockedReason, "nexal inference pin "+e.ID) {
			t.Errorf("%s reason = %q", e.ID, e.InstallBlockedReason)
		}
		if !repoName.MatchString(e.HFRepo) {
			t.Errorf("%s hfRepo %q", e.ID, e.HFRepo)
		}
		if e.RuntimeMaxInputTokens != 4096 || e.RuntimeMaxOutputTokens != 512 {
			t.Errorf("%s limits %d/%d", e.ID, e.RuntimeMaxInputTokens, e.RuntimeMaxOutputTokens)
		}
		if e.License == "" || e.ArchSource == "" {
			t.Errorf("%s lacks license/archSource", e.ID)
		}
		if i > 0 {
			p := c.Entries[i-1]
			if p.ParamsBillions < e.ParamsBillions || (p.ParamsBillions == e.ParamsBillions && p.BitsPerWeight < e.BitsPerWeight) {
				t.Errorf("catalog not sorted largest first at %s", e.ID)
			}
		}
	}
	if families["Qwen3"] != 12 || families["Phi-4"] != 2 {
		t.Errorf("families = %v", families)
	}
	for _, p := range []float64{0.6, 1.7, 4.0, 8.2, 14.8, 32.8, 3.8} {
		if !sizes[p] {
			t.Errorf("missing size %v", p)
		}
	}
	raw := string(catalogJSON)
	for _, bad := range []string{"http://", "https://", "huggingface", "sha256"} {
		if strings.Contains(raw, bad) {
			t.Errorf("catalog must not carry URLs (%q found)", bad)
		}
	}
}

func TestCatalogValidateRejects(t *testing.T) {
	base, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	clone := func(mut func(e *CatalogEntry)) CatalogFile {
		c := base
		c.Entries = append([]CatalogEntry(nil), base.Entries...)
		mut(&c.Entries[0])
		return c
	}
	goodRev := strings.Repeat("a", 40)
	goodFiles := fullPinFiles()
	tests := []struct {
		name string
		mut  func(e *CatalogEntry)
		ok   bool
	}{
		{"unmodified", func(e *CatalogEntry) {}, true},
		{"tampered estimate", func(e *CatalogEntry) { e.Estimate.WeightsBytes++ }, false},
		{"files on an unpinned entry", func(e *CatalogEntry) { e.Files = goodFiles }, false},
		{"revision on an unpinned entry", func(e *CatalogEntry) { e.Revision = goodRev }, false},
		{"bad hfRepo", func(e *CatalogEntry) { e.HFRepo = "../etc/passwd" }, false},
		{"installable without pin", func(e *CatalogEntry) { e.Installable = true }, false},
		{"pinned but claims installable with no files", func(e *CatalogEntry) { e.Pinned = true; e.Revision = goodRev; e.Installable = true }, false},
		{"pinned with a branch name", func(e *CatalogEntry) { e.Pinned = true; e.Revision = "main"; e.Files = goodFiles }, false},
		{"pinned with bad file hash", func(e *CatalogEntry) {
			e.Pinned, e.Revision, e.Files = true, goodRev, []PinFile{{Name: "config.json", Size: 1, SHA256: "zz"}}
		}, false},
		{"pinned with a .py file", func(e *CatalogEntry) {
			e.Pinned, e.Revision, e.Files = true, goodRev, []PinFile{{Name: "modeling.py", Size: 1, SHA256: strings.Repeat("c", 64)}}
		}, false},
		{"pinned complete and installable", func(e *CatalogEntry) {
			e.Pinned, e.Revision, e.Files, e.License, e.Installable, e.InstallBlockedReason = true, goodRev, goodFiles, "apache-2.0", true, ""
		}, true},
		{"pinned complete but installable=false", func(e *CatalogEntry) {
			e.Pinned, e.Revision, e.Files, e.License = true, goodRev, goodFiles, "apache-2.0"
		}, false},
		{"blocked without reason", func(e *CatalogEntry) { e.InstallBlockedReason = "" }, false},
		{"unknown architecture", func(e *CatalogEntry) { e.Architecture = "llama4" }, false},
		{"wrong token cap", func(e *CatalogEntry) { e.RuntimeMaxInputTokens = 131072 }, false},
		{"duplicate id", func(e *CatalogEntry) {}, true}, // replaced below
	}
	for _, tc := range tests[:len(tests)-1] {
		t.Run(tc.name, func(t *testing.T) {
			err := clone(tc.mut).Validate()
			if (err == nil) != tc.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	t.Run("duplicate id", func(t *testing.T) {
		c := clone(func(e *CatalogEntry) {})
		c.Entries[1].ID = c.Entries[0].ID
		if c.Validate() == nil {
			t.Error("duplicate ids accepted")
		}
	})
	t.Run("garbage json", func(t *testing.T) {
		if _, err := ParseCatalog([]byte(`{`)); err == nil {
			t.Error("garbage accepted")
		}
	})
}

func TestEmbeddedCatalogMatchesFormula(t *testing.T) {
	var c CatalogFile
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Entries {
		if want := EstimateRank(e.Arch, e.ParamsBillions, e.BitsPerWeight, 1); e.Estimate != want {
			t.Errorf("%s: catalog %+v, formula %+v (rerun: go run gen_catalog.go catalog.json)", e.ID, e.Estimate, want)
		}
	}
}

func TestMemoryFormulaKnownValues(t *testing.T) {
	if got := WeightsBytes(4.0, 4.5); got != 2_250_000_000 {
		t.Errorf("weights = %d", got)
	}
	qwen4b := ArchSpec{Layers: 36, KVHeads: 8, HeadDim: 128, HiddenSize: 2560, IntermediateSize: 9728, VocabSize: 151936}
	if got := KVBytes(qwen4b); got != 2*36*8*128*4608*2 {
		t.Errorf("kv = %d", got)
	}
	one := EstimateRank(qwen4b, 4.0, 4.5, 1)
	if one.SafetyBytes < safetyFloorBytes || one.TemporaryBytes != 225_000_000 {
		t.Errorf("one = %+v", one)
	}
	two := EstimateRank(qwen4b, 4.0, 4.5, 2)
	if two.WeightsBytes != 1_125_000_000 || two.KVBytes != one.KVBytes/2 {
		t.Errorf("two = %+v", two)
	}
	if two.TemporaryBytes <= two.WeightsBytes/10 {
		t.Errorf("sharded ranks must carry communication buffers: %+v", two)
	}
	if t1, _ := one.Total(); t1 <= 0 {
		t.Error("total")
	}
	var prev int64 = 1 << 62
	for k := 1; k <= 6; k++ {
		tot, err := EstimateRank(qwen4b, 4.0, 4.5, k).Total()
		if err != nil || tot >= prev {
			t.Errorf("k=%d total %d not below %d (%v)", k, tot, prev, err)
		}
		prev = tot
	}
	if EstimateRank(qwen4b, 4.0, 4.5, 0) != one {
		t.Error("ranks<1 must behave as 1")
	}
}

func fullPinFiles() []PinFile {
	h := func(c string) string { return strings.Repeat(c, 64) }
	return []PinFile{
		{Name: "config.json", Size: 700, SHA256: h("1")},
		{Name: "tokenizer.json", Size: 7000, SHA256: h("2")},
		{Name: "tokenizer_config.json", Size: 900, SHA256: h("3")},
		{Name: "model.safetensors", Size: 2_300_000_000, SHA256: h("4")},
	}
}
