package inference

import (
	"math"

	"nexal/connector/internal/pool"
)

// Runtime limits that bound every estimate. They mirror the MLX runtime profile
// (runtimes/QWEN-PHI.md: "retain the runtime's 4096 input and 512 output token
// limits, regardless of their advertised context capacity").
const (
	RuntimeMaxInputTokens  = 4096
	RuntimeMaxOutputTokens = 512
	// ContextTokens is the worst-case sequence the KV cache must hold.
	ContextTokens = RuntimeMaxInputTokens + RuntimeMaxOutputTokens
)

// Estimate constants. EVERY figure derived from these is an ESTIMATE, not a
// measurement. The runtime's own rule applies: measured load peak, resident
// weights, per-token KV, activations and communication buffers must be measured
// on hardware before any real release (runtimes/README.md); these exist so the
// preflight can reason before that measurement exists, and so a test can pin
// them.
const (
	// kvBytesPerElement: KV cache is kept in 16-bit floats. "Four-bit weights do
	// not mean four-bit KV memory" (runtimes/QWEN-PHI.md).
	kvBytesPerElement = 2
	// activationBytesPerElement: bf16/fp16 activations.
	activationBytesPerElement = 2
	// activationHiddenFactor and activationFFNFactor count the live
	// hidden-sized and feed-forward-sized tensors during prefill of ContextTokens.
	activationHiddenFactor = 6
	activationFFNFactor    = 2
	// logitsBytesPerElement: one position of fp32 logits over the vocabulary.
	logitsBytesPerElement = 4
	// loaderPeakFraction: transient bytes while safetensors are read, as a
	// fraction of the weights held by one rank.
	loaderPeakFraction = 0.10
	// commBufferFactor: ring all-reduce send/receive staging per rank, in units
	// of ContextTokens x hidden x 2 bytes. Only charged when sharded.
	commBufferFactor = 4
	// safetyFraction and safetyFloorBytes: the planner requires an explicit
	// positive safety margin. 10% of the subtotal, never under 1 GiB (interpreter,
	// tokenizer and allocator slack).
	safetyFraction   = 0.10
	safetyFloorBytes = int64(1) << 30
)

// ArchSpec is the architecture data memory estimates are computed from.
type ArchSpec struct {
	Layers           int `json:"layers"`
	KVHeads          int `json:"kvHeads"`
	HeadDim          int `json:"headDim"`
	HiddenSize       int `json:"hiddenSize"`
	IntermediateSize int `json:"intermediateSize"`
	VocabSize        int `json:"vocabSize"`
}

// WeightsBytes estimates stored weight bytes:
//
//	params x bitsPerWeight / 8
//
// bitsPerWeight is the EFFECTIVE rate. For MLX group quantization with group
// size 64 and a 16-bit scale and bias per group that is bits + 32/64, so 4-bit
// is 4.5 and 8-bit is 8.5. Embeddings are counted as ordinary weights.
func WeightsBytes(paramsBillions, bitsPerWeight float64) int64 {
	return int64(math.Ceil(paramsBillions * 1e9 * bitsPerWeight / 8))
}

// KVBytes estimates the KV cache for ContextTokens:
//
//	2 (K and V) x layers x kvHeads x headDim x ContextTokens x 2 bytes
func KVBytes(a ArchSpec) int64 {
	return 2 * int64(a.Layers) * int64(a.KVHeads) * int64(a.HeadDim) * ContextTokens * kvBytesPerElement
}

// ActivationBytes estimates prefill working memory:
//
//	ContextTokens x (6 x hidden + 2 x intermediate) x 2 bytes  +  vocab x 4 bytes
func ActivationBytes(a ArchSpec) int64 {
	perToken := int64(activationHiddenFactor*a.HiddenSize + activationFFNFactor*a.IntermediateSize)
	return ContextTokens*perToken*activationBytesPerElement + int64(a.VocabSize)*logitsBytesPerElement
}

func ceilDiv(n int64, d int) int64 { return (n + int64(d) - 1) / int64(d) }

// EstimateRank returns the planner input for ONE of ranks equal ranks. ranks==1
// is the single-host case. The split assumes an even partition of weights and
// KV across ranks and a full activation set per rank (conservative). The
// loader-peak term assumes a per-rank loader that reads only its own shard; the
// runtime has no such loader yet (runtimes/README.md "Integration boundaries
// still open"), so a real sharded peak could be higher: that is why a sharded
// layout is only ever a plan.
func EstimateRank(a ArchSpec, paramsBillions, bitsPerWeight float64, ranks int) pool.RankMemory {
	if ranks < 1 {
		ranks = 1
	}
	w := ceilDiv(WeightsBytes(paramsBillions, bitsPerWeight), ranks)
	kv := ceilDiv(KVBytes(a), ranks)
	act := ActivationBytes(a)
	tmp := int64(math.Ceil(float64(w) * loaderPeakFraction))
	if ranks > 1 {
		tmp += int64(commBufferFactor) * ContextTokens * int64(a.HiddenSize) * activationBytesPerElement
	}
	subtotal := w + kv + act + tmp
	safety := int64(math.Ceil(float64(subtotal) * safetyFraction))
	if safety < safetyFloorBytes {
		safety = safetyFloorBytes
	}
	return pool.RankMemory{WeightsBytes: w, KVBytes: kv, ActivationsBytes: act, TemporaryBytes: tmp, SafetyBytes: safety}
}
