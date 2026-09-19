package pool

import (
	"fmt"
	"sort"
	"strings"
)

type MLXBackend string

const (
	TCPRing MLXBackend = "tcp-ring"
	JACCL   MLXBackend = "jaccl"
)

// RankMemory must include loader peaks, duplicated weights and communication
// buffers in the relevant terms. It is not the shard's weight size alone.
type RankMemory struct {
	WeightsBytes     int64 `json:"weightsBytes"`
	KVBytes          int64 `json:"kvBytes"`
	ActivationsBytes int64 `json:"activationsBytes"`
	TemporaryBytes   int64 `json:"temporaryBytes"`
	SafetyBytes      int64 `json:"safetyBytes"`
}

func (m RankMemory) Total() (int64, error) {
	var total int64
	for _, n := range []int64{m.WeightsBytes, m.KVBytes, m.ActivationsBytes, m.TemporaryBytes, m.SafetyBytes} {
		if n < 0 || n > (1<<63-1)-total {
			return 0, ErrInvalid
		}
		total += n
	}
	if m.WeightsBytes <= 0 || m.SafetyBytes <= 0 {
		return 0, fmt.Errorf("%w: weights and explicit safety margin required", ErrInvalid)
	}
	return total, nil
}

type MLXPeer struct {
	DeviceID         string
	Trusted          bool
	Available        bool
	Chip             string // e.g. "M4", "M4 Pro"; never infer from memory size.
	MacOSMajor       int
	MacOSMinor       int
	Thunderbolt      int
	RDMAEnabled      bool
	ApprovedMemory   int64
	MeasuredHeadroom int64 // Already excludes OS/owner memory headroom.
	ReservedMemory   int64
	MLXVersion       string
	MLXLMVersion     string
	Backends         []MLXBackend
	DirectPeers      []string // Explicit fully connected topology for JACCL.
}

type PlacementRequest struct {
	ModelDigest       string
	Architecture      string
	Quantization      string
	PartitionStrategy string
	MLXVersion        string
	MLXLMVersion      string
	ValidatedModel    bool // Evidence supplied by the approved runtime registry.
	Backend           MLXBackend
	Ranks             []RankMemory // The approved partitioner supplies estimates.
	Peers             []MLXPeer
}

type RankPlacement struct {
	Rank          int        `json:"rank"`
	DeviceID      string     `json:"deviceId"`
	Memory        RankMemory `json:"memory"`
	RequiredBytes int64      `json:"requiredBytes"`
	UsableBytes   int64      `json:"usableBytes"`
}

type PlacementPlan struct {
	ModelDigest string          `json:"modelDigest"`
	Backend     MLXBackend      `json:"backend"`
	Ranks       []RankPlacement `json:"ranks"`
	// Always false: planning is not proof of runtime/topology correctness.
	ExecutionValidated bool `json:"executionValidated"`
}

func supports(p MLXPeer, backend MLXBackend) bool {
	for _, b := range p.Backends {
		if b == backend {
			return true
		}
	}
	return false
}

func direct(p MLXPeer, target string) bool {
	for _, id := range p.DirectPeers {
		if id == target {
			return true
		}
	}
	return false
}

// PlanMLX places the caller's validated rank partitions on distinct peers with
// per-rank headroom. It does not partition an arbitrary model or launch MLX.
// The largest-rank-first best-fit placement is deterministic, not an optimizer.
// JACCL conservatively requires all provided eligible peers to form a clique.
func PlanMLX(req PlacementRequest) (PlacementPlan, error) {
	if !validDigest(req.ModelDigest) || !req.ValidatedModel || req.Architecture == "" ||
		req.Quantization == "" || req.PartitionStrategy == "" || req.MLXVersion == "" ||
		req.MLXLMVersion == "" || len(req.Ranks) < 1 || len(req.Ranks) > 64 ||
		len(req.Peers) < len(req.Ranks) || len(req.Peers) > 128 ||
		(req.Backend != TCPRing && req.Backend != JACCL) {
		return PlacementPlan{}, ErrUnsupported
	}
	type availablePeer struct {
		peer  MLXPeer
		bytes int64
	}
	var peers []availablePeer
	seen := map[string]bool{}
	for _, p := range req.Peers {
		if !validDigest(p.DeviceID) || seen[p.DeviceID] {
			return PlacementPlan{}, ErrInvalid
		}
		seen[p.DeviceID] = true
		if !p.Trusted || !p.Available || !supports(p, req.Backend) ||
			p.MLXVersion != req.MLXVersion || p.MLXLMVersion != req.MLXLMVersion {
			continue
		}
		if p.ApprovedMemory < 0 || p.MeasuredHeadroom < 0 || p.ReservedMemory < 0 {
			return PlacementPlan{}, ErrInvalid
		}
		if req.Backend == JACCL {
			// A base M4 is ineligible even if capability inventory mistakenly
			// claims TB5. TB5/macOS/RDMA are necessary, not sufficient evidence.
			chip := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p.Chip)), "apple"))
			if chip == "m4" || p.Thunderbolt < 5 ||
				!p.RDMAEnabled || p.MacOSMajor < 26 || (p.MacOSMajor == 26 && p.MacOSMinor < 2) {
				continue
			}
		}
		// Both limits describe a pre-reservation budget. Subtract commitments
		// from their minimum to avoid granting memory already promised.
		usable := p.ApprovedMemory
		if p.MeasuredHeadroom < usable {
			usable = p.MeasuredHeadroom
		}
		if p.ReservedMemory > usable {
			continue
		}
		peers = append(peers, availablePeer{p, usable - p.ReservedMemory})
	}
	if len(peers) < len(req.Ranks) {
		return PlacementPlan{}, fmt.Errorf("%w: insufficient compatible peers", ErrUnsupported)
	}
	if req.Backend == JACCL {
		for _, a := range peers {
			for _, b := range peers {
				if a.peer.DeviceID != b.peer.DeviceID && !direct(a.peer, b.peer.DeviceID) {
					return PlacementPlan{}, fmt.Errorf("%w: JACCL requires direct fully connected peers", ErrUnsupported)
				}
			}
		}
	}
	type rankSize struct {
		rank int
		size int64
	}
	ranks := make([]rankSize, len(req.Ranks))
	for idx, m := range req.Ranks {
		size, err := m.Total()
		if err != nil {
			return PlacementPlan{}, err
		}
		ranks[idx] = rankSize{idx, size}
	}
	sort.Slice(ranks, func(i, j int) bool {
		if ranks[i].size == ranks[j].size {
			return ranks[i].rank < ranks[j].rank
		}
		return ranks[i].size > ranks[j].size
	})
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].bytes == peers[j].bytes {
			return peers[i].peer.DeviceID < peers[j].peer.DeviceID
		}
		return peers[i].bytes < peers[j].bytes
	})
	plan := PlacementPlan{ModelDigest: req.ModelDigest, Backend: req.Backend, Ranks: make([]RankPlacement, len(ranks))}
	for _, rank := range ranks {
		found := -1
		for idx, peer := range peers {
			if peer.bytes >= rank.size {
				found = idx
				break
			}
		}
		if found < 0 {
			return PlacementPlan{}, fmt.Errorf("%w: rank %d needs %d bytes", ErrQuota, rank.rank, rank.size)
		}
		p := peers[found]
		plan.Ranks[rank.rank] = RankPlacement{rank.rank, p.peer.DeviceID, req.Ranks[rank.rank], rank.size, p.bytes}
		peers = append(peers[:found], peers[found+1:]...)
	}
	return plan, nil
}
