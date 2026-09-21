package pool

import (
	"encoding/binary"
	"fmt"
	"math"
)

// The ring collective's frame codec. It is hand-rolled for the same reason the
// DNS codec in internal/discovery/wire.go is: this module has zero third-party
// dependencies, and a fixed-layout binary frame is a bounded, reviewable amount
// of encoding.
//
// The bound discipline is copied from that file deliberately. Every length in a
// frame is supplied by the sender, so no length is ever used to size an
// allocation before it has been checked against both a hard ceiling and the
// bytes actually present. A frame that does not add up is refused whole; there
// is no partial read and no "nearly valid" path.
//
// Unlike discovery, the sender here is already authenticated: a frame only
// reaches this decoder through the mutual-TLS, fingerprint-pinned peer
// transport, from a device the coordinator authorized and the registry still
// lists as an unrevoked contributor. That narrows who can send hostile bytes to
// an enrolled machine; it does not make the bytes trustworthy, because an
// enrolled machine can be compromised, out of date, or simply buggy.
//
// This layout crosses no repository boundary. Both ends of a ring run this same
// Go code from this same module, so there is no independently deployed peer that
// could drift from it — the hazard HARDENING-PLAN §43 describes, and the reason
// no constant here is copied from or shared with the coordinator.
const (
	// collectiveMagic is ASCII "NXCL". It is checked first so a frame from
	// something that is not this protocol is refused before any field is read.
	collectiveMagic   uint32 = 0x4e58434c
	collectiveVersion uint8  = 1

	// collectiveHeaderBytes is the fixed header length. Every offset below is
	// written out rather than computed from struct layout, because a layout that
	// is implied by Go types is a layout that changes when somebody reorders a
	// struct field.
	//
	//	 0 magic       uint32
	//	 4 version     uint8
	//	 5 kind        uint8
	//	 6 phase       uint8
	//	 7 op          uint8
	//	 8 senderRank  uint16
	//	10 step        uint16
	//	12 chunk       uint16
	//	14 tenantLen   uint8
	//	15 reasonLen   uint8
	//	16 failedRank  uint16
	//	18 total       uint32   (values in the whole vector)
	//	22 count       uint32   (values in this frame's chunk)
	//	26 session     [64]byte (64 lowercase hex characters, ASCII)
	//	90 tenant      [tenantLen]byte
	//	   reason      [reasonLen]byte
	//	   values      [count]float64, IEEE-754 big endian
	collectiveHeaderBytes = 90

	collectiveKindChunk uint8 = 1
	collectiveKindAbort uint8 = 2

	// The two phases of a ring all-reduce. They are carried in the frame so a
	// receiver never has to infer which half of the collective a chunk belongs
	// to from a step number alone.
	collectivePhaseReduceScatter uint8 = 1
	collectivePhaseAllGather     uint8 = 2

	// MaxRingRanks bounds ring size. ReplicateProtected already caps a fan-out
	// at 16 peers, and §37.3 records one job per donor on a LAN, so a ring
	// larger than this is not a bigger fleet — it is a request to hold 2·N
	// buffers for a topology nobody has run. Raising it is a deliberate change
	// with the memory arithmetic below redone, not a constant edit.
	MaxRingRanks = 16
	// MaxCollectiveValues bounds the whole vector, in float64 elements: 2 MiB of
	// payload. A chunk is total/N, and a rank may hold at most the frames of one
	// full phase from a predecessor it has not drained, so worst-case pending
	// payload per session stays under 2·MaxCollectiveValues·8 = 4 MiB, which is
	// what maxCollectivePendingBytes enforces.
	MaxCollectiveValues = 1 << 18
	// maxCollectiveTenant bounds the tenant scope string, and
	// maxCollectiveReason bounds an abort reason. Both are attacker-influenced
	// text that may end up in a log line, so they are additionally restricted to
	// printable ASCII by validCollectiveText.
	maxCollectiveTenant = 64
	maxCollectiveReason = 64
	// maxCollectiveFrameBytes is the single datagram-equivalent bound: an HTTP
	// body larger than this is refused before it is read, not after.
	maxCollectiveFrameBytes = collectiveHeaderBytes + maxCollectiveTenant +
		maxCollectiveReason + 8*MaxCollectiveValues
)

// collectiveFrame is the decoded form. Nothing in it is trusted by virtue of
// having decoded: the transport checks the sender's rank against the
// fingerprint it authenticated, and AllReduce checks every field against what
// it expected for that step.
type collectiveFrame struct {
	kind       uint8
	phase      uint8
	op         ReduceOp
	senderRank int
	step       int
	chunk      int
	failedRank int
	session    string
	tenant     string
	reason     string
	total      int
	values     []float64
}

// validCollectiveText bounds a text field and refuses control characters, so a
// decoded tenant scope or abort reason cannot smuggle a newline into a log line
// or a terminal escape into a UI. It is the same rule as discovery's
// validTXTValue, duplicated rather than shared for the same reason that file
// gives: neither package should be able to loosen the other's check by editing
// its own.
func validCollectiveText(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// encodeCollectiveFrame refuses to produce a frame it would itself reject. A
// caller cannot send an oversized or inconsistent frame and discover the problem
// as a peer's HTTP error.
func encodeCollectiveFrame(f collectiveFrame) ([]byte, error) {
	if f.kind != collectiveKindChunk && f.kind != collectiveKindAbort {
		return nil, ErrInvalid
	}
	if !validDigest(f.session) || !validCollectiveText(f.tenant, maxCollectiveTenant) ||
		f.senderRank < 0 || f.senderRank >= MaxRingRanks ||
		f.step < 0 || f.step >= MaxRingRanks || f.chunk < 0 || f.chunk >= MaxRingRanks ||
		f.failedRank < 0 || f.failedRank >= MaxRingRanks ||
		f.total < 0 || f.total > MaxCollectiveValues || len(f.values) > f.total {
		return nil, ErrInvalid
	}
	switch f.kind {
	case collectiveKindChunk:
		if f.phase != collectivePhaseReduceScatter && f.phase != collectivePhaseAllGather {
			return nil, ErrInvalid
		}
		if !f.op.valid() || len(f.values) == 0 || f.reason != "" {
			return nil, ErrInvalid
		}
	case collectiveKindAbort:
		// An abort carries no payload and no phase: it says which rank failed
		// and why, and nothing a survivor could mistake for data.
		if f.phase != 0 || f.op != 0 || f.total != 0 || len(f.values) != 0 ||
			!validCollectiveText(f.reason, maxCollectiveReason) {
			return nil, ErrInvalid
		}
	}
	// Sized to exactly this frame, not to the ceiling: every step allocates one of
	// these, and reserving the maximum for a small chunk would make the common case
	// pay for the worst one.
	size := collectiveHeaderBytes + len(f.tenant) + len(f.reason) + 8*len(f.values)
	buf := make([]byte, collectiveHeaderBytes, size)
	binary.BigEndian.PutUint32(buf[0:4], collectiveMagic)
	buf[4] = collectiveVersion
	buf[5] = f.kind
	buf[6] = f.phase
	buf[7] = uint8(f.op)
	binary.BigEndian.PutUint16(buf[8:10], uint16(f.senderRank))
	binary.BigEndian.PutUint16(buf[10:12], uint16(f.step))
	binary.BigEndian.PutUint16(buf[12:14], uint16(f.chunk))
	buf[14] = uint8(len(f.tenant))
	buf[15] = uint8(len(f.reason))
	binary.BigEndian.PutUint16(buf[16:18], uint16(f.failedRank))
	binary.BigEndian.PutUint32(buf[18:22], uint32(f.total))
	binary.BigEndian.PutUint32(buf[22:26], uint32(len(f.values)))
	copy(buf[26:90], f.session)
	buf = append(buf, f.tenant...)
	buf = append(buf, f.reason...)
	for _, v := range f.values {
		buf = binary.BigEndian.AppendUint64(buf, math.Float64bits(v))
	}
	if len(buf) > maxCollectiveFrameBytes {
		return nil, fmt.Errorf("%w: collective frame exceeds %d bytes", ErrInvalid, maxCollectiveFrameBytes)
	}
	return buf, nil
}

// decodeCollectiveFrame parses one frame with hard bounds on every length the
// frame itself supplies.
//
// The order of the checks is the point. The declared value count is compared
// against MaxCollectiveValues, and then the frame's total length is required to
// equal the header plus the declared text plus exactly count·8 payload bytes,
// before a single element is allocated. A frame claiming four billion values
// therefore costs one comparison, not an allocation.
func decodeCollectiveFrame(buf []byte) (collectiveFrame, error) {
	if len(buf) < collectiveHeaderBytes || len(buf) > maxCollectiveFrameBytes {
		return collectiveFrame{}, ErrInvalid
	}
	if binary.BigEndian.Uint32(buf[0:4]) != collectiveMagic || buf[4] != collectiveVersion {
		return collectiveFrame{}, ErrInvalid
	}
	f := collectiveFrame{
		kind:       buf[5],
		phase:      buf[6],
		op:         ReduceOp(buf[7]),
		senderRank: int(binary.BigEndian.Uint16(buf[8:10])),
		step:       int(binary.BigEndian.Uint16(buf[10:12])),
		chunk:      int(binary.BigEndian.Uint16(buf[12:14])),
		failedRank: int(binary.BigEndian.Uint16(buf[16:18])),
		total:      int(binary.BigEndian.Uint32(buf[18:22])),
		session:    string(buf[26:90]),
	}
	count := int(binary.BigEndian.Uint32(buf[22:26]))
	tenantLen, reasonLen := int(buf[14]), int(buf[15])
	if count > MaxCollectiveValues || f.total > MaxCollectiveValues || count > f.total {
		return collectiveFrame{}, ErrInvalid
	}
	// Exact length: no trailing bytes are tolerated. A frame is a single object
	// over a stream transport, so extra bytes are either a different protocol or
	// an attempt to hide something behind a valid-looking prefix.
	if len(buf) != collectiveHeaderBytes+tenantLen+reasonLen+8*count {
		return collectiveFrame{}, ErrInvalid
	}
	if f.senderRank >= MaxRingRanks || f.step >= MaxRingRanks ||
		f.chunk >= MaxRingRanks || f.failedRank >= MaxRingRanks {
		return collectiveFrame{}, ErrInvalid
	}
	if !validDigest(f.session) {
		return collectiveFrame{}, ErrInvalid
	}
	off := collectiveHeaderBytes
	f.tenant = string(buf[off : off+tenantLen])
	off += tenantLen
	f.reason = string(buf[off : off+reasonLen])
	off += reasonLen
	if !validCollectiveText(f.tenant, maxCollectiveTenant) {
		return collectiveFrame{}, ErrInvalid
	}
	switch f.kind {
	case collectiveKindChunk:
		if (f.phase != collectivePhaseReduceScatter && f.phase != collectivePhaseAllGather) ||
			!f.op.valid() || count == 0 || reasonLen != 0 {
			return collectiveFrame{}, ErrInvalid
		}
		f.values = make([]float64, count)
		for i := range f.values {
			f.values[i] = math.Float64frombits(binary.BigEndian.Uint64(buf[off+8*i : off+8*i+8]))
		}
	case collectiveKindAbort:
		if f.phase != 0 || f.op != 0 || f.total != 0 || count != 0 ||
			!validCollectiveText(f.reason, maxCollectiveReason) {
			return collectiveFrame{}, ErrInvalid
		}
	default:
		return collectiveFrame{}, ErrInvalid
	}
	return f, nil
}
