package inference

import (
	"math"
	"testing"
)

func TestKVAndActivationBytesFormulas(t *testing.T) {
	a := ArchSpec{Layers: 36, KVHeads: 8, HeadDim: 128, HiddenSize: 2560, IntermediateSize: 9728, VocabSize: 151936}
	if got, want := KVBytes(a), int64(2*36*8*128*ContextTokens*2); got != want {
		t.Errorf("KVBytes = %d, want %d", got, want)
	}
	perToken := int64(6*2560 + 2*9728)
	if got, want := ActivationBytes(a), ContextTokens*perToken*2+151936*4; got != want {
		t.Errorf("ActivationBytes = %d, want %d", got, want)
	}
}

func TestEstimateRankClampsRanksAndAppliesSafetyFloor(t *testing.T) {
	a := ArchSpec{Layers: 2, KVHeads: 1, HeadDim: 8, HiddenSize: 16, IntermediateSize: 32, VocabSize: 100}
	for _, ranks := range []int{0, -3, 1} {
		got := EstimateRank(a, 0.001, 4.5, ranks)
		if got != EstimateRank(a, 0.001, 4.5, 1) {
			t.Errorf("ranks=%d should behave as single host, got %+v", ranks, got)
		}
		if got.SafetyBytes != safetyFloorBytes {
			t.Errorf("tiny model safety = %d, want floor %d", got.SafetyBytes, safetyFloorBytes)
		}
	}
}

func TestEstimateRankShardingSplitsWeightsAndChargesComm(t *testing.T) {
	a := ArchSpec{Layers: 36, KVHeads: 8, HeadDim: 128, HiddenSize: 2560, IntermediateSize: 9728, VocabSize: 151936}
	one := EstimateRank(a, 8, 4.5, 1)
	two := EstimateRank(a, 8, 4.5, 2)
	if two.WeightsBytes != ceilDiv(one.WeightsBytes, 2) || two.KVBytes != ceilDiv(one.KVBytes, 2) {
		t.Errorf("weights/KV not halved: one=%+v two=%+v", one, two)
	}
	if two.ActivationsBytes != one.ActivationsBytes {
		t.Errorf("activations must stay full per rank: %d vs %d", two.ActivationsBytes, one.ActivationsBytes)
	}
	comm := int64(commBufferFactor) * ContextTokens * 2560 * 2
	loader := func(w int64) int64 { return int64(math.Ceil(float64(w) * loaderPeakFraction)) }
	if one.TemporaryBytes != loader(one.WeightsBytes) {
		t.Errorf("single host temporary = %d, want loader peak only %d", one.TemporaryBytes, loader(one.WeightsBytes))
	}
	if want := loader(two.WeightsBytes) + comm; two.TemporaryBytes != want {
		t.Errorf("sharded temporary = %d, want %d (loader peak + comm)", two.TemporaryBytes, want)
	}
	if sub := one.WeightsBytes + one.KVBytes + one.ActivationsBytes + one.TemporaryBytes; one.SafetyBytes < sub/10 {
		t.Errorf("safety %d under 10%% of subtotal %d", one.SafetyBytes, sub)
	}
}
