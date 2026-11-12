package inference

import (
	"strings"
	"testing"

	"nexal/connector/internal/pool"
)

func manifestEntry() CatalogEntry {
	return CatalogEntry{
		ID: "m/x", Revision: "abc", License: "apache-2.0", Architecture: "llama",
		Installable: true,
		Estimate: pool.RankMemory{WeightsBytes: 1000, KVBytes: ContextTokens*2 + 1, ActivationsBytes: 5,
			TemporaryBytes: 7, SafetyBytes: 3},
		Files: []PinFile{
			{Name: "model.safetensors", Size: 400, SHA256: "aa"},
			{Name: "config.json", Size: 10, SHA256: "bb"},
		},
	}
}

func TestBuildManifestNotInstallable(t *testing.T) {
	e := manifestEntry()
	e.Installable = false
	e.InstallBlockedReason = "unpinned"
	if _, _, err := BuildManifest(e); err == nil || !strings.Contains(err.Error(), "unpinned") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildManifestEstimateAndKVRoundsUp(t *testing.T) {
	m, warns, err := BuildManifest(manifestEntry())
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if m.Memory.WeightsBytes != 1000 || m.Memory.KVBytesPerToken != 3 || m.Memory.LoadPeakBytes != 1007 {
		t.Fatalf("memory = %+v", m.Memory)
	}
	if m.SchemaVersion != 1 || m.ModelType != "llama" || m.Files["config.json"] != "bb" {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestBuildManifestWeightFilesExceedEstimate(t *testing.T) {
	e := manifestEntry()
	e.Files[0].Size = 5000
	m, warns, err := BuildManifest(e)
	if err != nil || len(warns) != 1 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if m.Memory.WeightsBytes != 5000 || m.Memory.LoadPeakBytes != 5007 {
		t.Fatalf("memory = %+v", m.Memory)
	}
}

func TestBuildManifestRejectsNonPositiveTerm(t *testing.T) {
	e := manifestEntry()
	e.Estimate.SafetyBytes = 0
	if _, _, err := BuildManifest(e); err == nil || !strings.Contains(err.Error(), "safety_bytes") {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestBytesCanonical(t *testing.T) {
	m, _, _ := BuildManifest(manifestEntry())
	b, err := m.Bytes()
	if err != nil || !strings.HasSuffix(string(b), "}\n") {
		t.Fatalf("bytes=%q err=%v", b, err)
	}
	if strings.Index(string(b), `"config.json"`) > strings.Index(string(b), `"model.safetensors"`) {
		t.Fatal("file keys not sorted")
	}
}
