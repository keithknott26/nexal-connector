package inference

import (
	"strings"
	"testing"
)

func TestPinProblems(t *testing.T) {
	sha := strings.Repeat("a", 64)
	rev := strings.Repeat("b", 40)
	f := func(name string) PinFile { return PinFile{Name: name, Size: 10, SHA256: sha} }
	good := func() CatalogEntry {
		return CatalogEntry{
			Pinned: true, Revision: rev, HFRepo: "org/model", License: "apache-2.0",
			Files: []PinFile{f("config.json"), f("tokenizer.json"), f("tokenizer_config.json"), f("model.safetensors")},
		}
	}

	if p := good().PinProblems(); len(p) != 0 {
		t.Fatalf("complete pin should have no problems, got %v", p)
	}

	if p := (CatalogEntry{}).PinProblems(); len(p) != 2 || p[0] != "revision (40-hex commit)" {
		t.Fatalf("unpinned entry: got %v", p)
	}

	e := good()
	e.Revision, e.HFRepo, e.License = "short", "bad repo", ""
	p := e.PinProblems()
	want := []string{"revision (40-hex commit)", "hfRepo", "license"}
	if len(p) != len(want) {
		t.Fatalf("got %v want %v", p, want)
	}
	for i := range want {
		if p[i] != want[i] {
			t.Fatalf("order not stable: got %v want %v", p, want)
		}
	}

	e = good()
	e.Files = e.Files[1:3] // drop config.json and the weights
	p = e.PinProblems()
	joined := strings.Join(p, "|")
	for _, s := range []string{"of config.json", "the .safetensors weights"} {
		if !strings.Contains(joined, s) {
			t.Errorf("missing %q in %v", s, p)
		}
	}

	e = good()
	e.Files[0].SHA256 = "nothex"
	if !strings.Contains(strings.Join(e.PinProblems(), "|"), "of config.json") {
		t.Errorf("bad sha should flag config.json: %v", e.PinProblems())
	}
}
