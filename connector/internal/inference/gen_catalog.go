//go:build ignore

// Command gen_catalog regenerates catalog.json from the documented formula:
//
//	go run gen_catalog.go catalog.json
//
// It exists so the checked-in estimates cannot drift from memory.go (a test also
// enforces it). It writes no revision, hash or URL: pinning is a reviewed edit.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"nexal/connector/internal/inference"
)

type spec struct {
	id, family, name, arch, repo string
	params                       float64
	a                            inference.ArchSpec
	lic                          string
	ctx                          int
	src                          string
}

func main() {
	qsrc := "Qwen3 dense model card figures, recalled; NOT read from a pinned config.json. Re-check at pin time."
	psrc := "Phi-4-mini-instruct model card figures, recalled; NOT read from a pinned config.json. Re-check at pin time."
	specs := []spec{
		{"qwen3-32b", "Qwen3", "Qwen3-32B", "qwen3", "mlx-community/Qwen3-32B", 32.8, inference.ArchSpec{Layers: 64, KVHeads: 8, HeadDim: 128, HiddenSize: 5120, IntermediateSize: 25600, VocabSize: 151936}, "Apache-2.0", 32768, qsrc},
		{"qwen3-14b", "Qwen3", "Qwen3-14B", "qwen3", "mlx-community/Qwen3-14B", 14.8, inference.ArchSpec{Layers: 40, KVHeads: 8, HeadDim: 128, HiddenSize: 5120, IntermediateSize: 17408, VocabSize: 151936}, "Apache-2.0", 32768, qsrc},
		{"qwen3-8b", "Qwen3", "Qwen3-8B", "qwen3", "mlx-community/Qwen3-8B", 8.2, inference.ArchSpec{Layers: 36, KVHeads: 8, HeadDim: 128, HiddenSize: 4096, IntermediateSize: 12288, VocabSize: 151936}, "Apache-2.0", 32768, qsrc},
		{"qwen3-4b", "Qwen3", "Qwen3-4B", "qwen3", "mlx-community/Qwen3-4B", 4.0, inference.ArchSpec{Layers: 36, KVHeads: 8, HeadDim: 128, HiddenSize: 2560, IntermediateSize: 9728, VocabSize: 151936}, "Apache-2.0", 32768, qsrc},
		{"qwen3-1.7b", "Qwen3", "Qwen3-1.7B", "qwen3", "mlx-community/Qwen3-1.7B", 1.7, inference.ArchSpec{Layers: 28, KVHeads: 8, HeadDim: 128, HiddenSize: 2048, IntermediateSize: 6144, VocabSize: 151936}, "Apache-2.0", 32768, qsrc},
		{"qwen3-0.6b", "Qwen3", "Qwen3-0.6B", "qwen3", "mlx-community/Qwen3-0.6B", 0.6, inference.ArchSpec{Layers: 28, KVHeads: 8, HeadDim: 128, HiddenSize: 1024, IntermediateSize: 3072, VocabSize: 151936}, "Apache-2.0", 32768, qsrc},
		{"phi-4-mini", "Phi-4", "Phi-4-mini-instruct", "phi3", "mlx-community/Phi-4-mini-instruct", 3.8, inference.ArchSpec{Layers: 32, KVHeads: 8, HeadDim: 128, HiddenSize: 3072, IntermediateSize: 8192, VocabSize: 200064}, "MIT", 131072, psrc},
	}
	quants := []struct {
		id, label, repoSuffix string
		bpw                   float64
	}{{"q4", "4-bit group-64", "-4bit", 4.5}, {"q8", "8-bit group-64", "-8bit", 8.5}}
	out := map[string]any{
		"schemaVersion": 1, "runtime": "nexal_mlx 0.2.0 (qwen3/phi3 profile)",
		"mlxVersion": "0.29.3", "mlxLmVersion": "0.28.4",
		"runtimeMaxInputTokens": inference.RuntimeMaxInputTokens, "runtimeMaxOutputTokens": inference.RuntimeMaxOutputTokens,
		"estimateLabel": "ESTIMATE: derived from parameter count and bits per weight, not measured on hardware.",
		"formula":       "weights=params*bpw/8; kv=2*layers*kvHeads*headDim*4608*2B; activations=4608*(6*hidden+2*intermediate)*2B+vocab*4B; temporary=0.10*weights (+4*4608*hidden*2B comm buffers per rank when sharded); safety=max(1GiB,0.10*subtotal). See memory.go.",
	}
	var entries []map[string]any
	for _, s := range specs {
		for _, q := range quants {
			est := inference.EstimateRank(s.a, s.params, q.bpw, 1)
			entries = append(entries, map[string]any{
				"id": s.id + "-" + q.id, "family": s.family, "displayName": s.name + " (" + q.label + ")",
				"architecture": s.arch, "paramsBillions": s.params, "quantization": q.label, "bitsPerWeight": q.bpw,
				"hfRepo": s.repo + q.repoSuffix, "arch": s.a, "archSource": s.src, "estimate": est, "license": s.lic,
				"advertisedContextTokens": s.ctx, "runtimeMaxInputTokens": inference.RuntimeMaxInputTokens, "runtimeMaxOutputTokens": inference.RuntimeMaxOutputTokens,
				"pinned": false, "revision": "", "files": []inference.PinFile{}, "installable": false,
				"installBlockedReason": "revision and per-file SHA-256 not yet pinned",
			})
		}
	}
	out["entries"] = entries
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[1], append(b, '\n'), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("ok", len(entries))
}
