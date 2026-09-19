package pool

import (
	"errors"
	"testing"
)

func placementFixture() PlacementRequest {
	ids := []string{digestOf([]byte("first")), digestOf([]byte("second"))}
	return PlacementRequest{
		ModelDigest: digestOf([]byte("model")), Architecture: "approved-test-model",
		Quantization: "q4", PartitionStrategy: "pipeline-v1",
		MLXVersion: "pinned-mlx", MLXLMVersion: "pinned-mlx-lm", ValidatedModel: true, Backend: TCPRing,
		Ranks: []RankMemory{{600, 100, 50, 50, 100}, {400, 100, 50, 50, 100}},
		Peers: []MLXPeer{
			{DeviceID: ids[0], Trusted: true, Available: true, Chip: "M4", MacOSMajor: 26, MacOSMinor: 2,
				Thunderbolt: 4, ApprovedMemory: 1000, MeasuredHeadroom: 1000,
				MLXVersion: "pinned-mlx", MLXLMVersion: "pinned-mlx-lm", Backends: []MLXBackend{TCPRing, JACCL}},
			{DeviceID: ids[1], Trusted: true, Available: true, Chip: "M4 Pro", MacOSMajor: 26, MacOSMinor: 2,
				Thunderbolt: 5, RDMAEnabled: true, ApprovedMemory: 800, MeasuredHeadroom: 800,
				MLXVersion: "pinned-mlx", MLXLMVersion: "pinned-mlx-lm", Backends: []MLXBackend{TCPRing, JACCL}},
		},
	}
}

func TestMLXRankMemoryAndBackendPlacement(t *testing.T) {
	req := placementFixture()
	plan, err := PlanMLX(req)
	if err != nil || len(plan.Ranks) != 2 || plan.ExecutionValidated {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if plan.Ranks[0].RequiredBytes != 900 || plan.Ranks[1].RequiredBytes != 700 {
		t.Fatal("memory terms missing")
	}
	req.Peers[0].ReservedMemory = 101
	if _, err := PlanMLX(req); !errors.Is(err, ErrQuota) {
		t.Fatalf("rank exceeded memory: %v", err)
	}
	req = placementFixture()
	req.Ranks[0] = RankMemory{1000, 0, 0, 0, 1}
	if _, err := PlanMLX(req); !errors.Is(err, ErrQuota) {
		t.Fatal("aggregate memory disguised infeasible individual rank")
	}
	req = placementFixture()
	req.Backend = JACCL
	req.Peers[0].Thunderbolt = 5 // Misreported flag must not override base M4.
	req.Peers[0].RDMAEnabled = true
	if _, err := PlanMLX(req); !errors.Is(err, ErrUnsupported) {
		t.Fatal("base M4 accepted JACCL")
	}
	req.Peers[0].Chip = "Apple M4"
	if _, err := PlanMLX(req); !errors.Is(err, ErrUnsupported) {
		t.Fatal("alternate base M4 label accepted JACCL")
	}
	req.Peers[0].Chip = "M4 Pro"
	if _, err := PlanMLX(req); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unconnected JACCL topology accepted")
	}
	req.Peers[0].DirectPeers = []string{req.Peers[1].DeviceID}
	req.Peers[1].DirectPeers = []string{req.Peers[0].DeviceID}
	if _, err := PlanMLX(req); err != nil {
		t.Fatal(err)
	}
	req.Peers[1].MLXVersion = "different"
	if _, err := PlanMLX(req); !errors.Is(err, ErrUnsupported) {
		t.Fatal("mixed runtimes accepted")
	}
}

func TestMemoryOverflowSafetyAndUnsupportedModel(t *testing.T) {
	if _, err := (RankMemory{1<<63 - 1, 1, 0, 0, 1}).Total(); !errors.Is(err, ErrInvalid) {
		t.Fatal("memory sum overflow")
	}
	if _, err := (RankMemory{1, 0, 0, 0, 0}).Total(); !errors.Is(err, ErrInvalid) {
		t.Fatal("no safety margin")
	}
	req := placementFixture()
	req.ValidatedModel = false
	if _, err := PlanMLX(req); !errors.Is(err, ErrUnsupported) {
		t.Fatal("arbitrary model accepted")
	}
}
