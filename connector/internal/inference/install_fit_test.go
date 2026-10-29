package inference

import (
	"encoding/json"
	"strings"
	"testing"

	"nexal/connector/internal/pool"
)

func TestCheckLocalFit(t *testing.T) {
	const gibB = 1 << 30
	entry := CatalogEntry{ID: "m", DisplayName: "Model", Estimate: pool.RankMemory{WeightsBytes: 7 * gibB, SafetyBytes: gibB}}
	self := func(mem uint64) Report {
		return Report{Machines: []MachineReport{{ID: "peer", TotalMemoryBytes: 1 << 40}, {ID: "me", IsSelf: true, TotalMemoryBytes: mem}}}
	}

	cases := []struct {
		name  string
		r     Report
		e     CatalogEntry
		code  string
		model string
	}{
		{"fits with headroom", self(16 * gibB), entry, "", "me"},
		{"exactly at the 80% limit fits", self(10 * gibB), entry, "", "me"},
		{"just over the limit does not fit", self(10*gibB - 1), entry, "does_not_fit", "me"},
		{"unknown memory", self(0), entry, "host_unknown", "me"},
		{"invalid estimate", self(64 * gibB), CatalogEntry{ID: "bad"}, "internal", "me"},
		{"no self machine ignores peers", Report{Machines: []MachineReport{{ID: "peer", TotalMemoryBytes: 1 << 40}}}, entry, "host_unknown", ""},
		{"empty report", Report{}, entry, "host_unknown", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := checkLocalFit(c.r, c.e)
			if c.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || err.Code != c.code {
				t.Fatalf("want code %q, got %v", c.code, err)
			}
			if m.ID != c.model {
				t.Fatalf("machine = %q, want %q", m.ID, c.model)
			}
		})
	}
}

func TestSortedNames(t *testing.T) {
	if got := sortedNames(nil); len(got) != 0 {
		t.Fatalf("nil files: %v", got)
	}
	got := sortedNames([]PinFile{{Name: "weights.safetensors"}, {Name: "config.json"}, {Name: "tokenizer.json"}})
	want := []string{"config.json", "tokenizer.json", "weights.safetensors"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSha256Hex(t *testing.T) {
	// FIPS 180-2 test vectors.
	for in, want := range map[string]string{
		"":    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"abc": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	} {
		if got := sha256Hex([]byte(in)); got != want {
			t.Errorf("sha256Hex(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestManifestBytesSortedKeysAndRoundTrip(t *testing.T) {
	m := Manifest{
		SchemaVersion: 1, ModelID: "m", Revision: "r", License: "mit", ModelType: "qwen3",
		Files:  map[string]string{"z.json": "bb", "a.json": "aa", "m.safetensors": "cc"},
		Memory: ManifestMemory{WeightsBytes: 1, KVBytesPerToken: 2, ActivationBytes: 3, BufferBytes: 4, LoadPeakBytes: 5, SafetyBytes: 6},
	}
	b, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	a, mm, z := strings.Index(s, `"a.json"`), strings.Index(s, `"m.safetensors"`), strings.Index(s, `"z.json"`)
	if a < 0 || !(a < mm && mm < z) {
		t.Errorf("file keys not sorted in %s", s)
	}
	var back Manifest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Memory != m.Memory || back.ModelID != m.ModelID || len(back.Files) != 3 || back.Files["a.json"] != "aa" {
		t.Errorf("round trip changed the manifest: %+v", back)
	}
}
