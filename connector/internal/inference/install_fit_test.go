package inference

import (
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
