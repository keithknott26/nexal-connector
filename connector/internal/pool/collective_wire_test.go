package pool

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

const testSession = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testChunkFrame() collectiveFrame {
	return collectiveFrame{
		kind: collectiveKindChunk, phase: collectivePhaseReduceScatter, op: ReduceSum,
		senderRank: 2, step: 1, chunk: 3, session: testSession, tenant: "tenant-a",
		total: 6, values: []float64{1.5, -2, math.Pi},
	}
}

func TestCollectiveFrameRoundTripPreservesEveryField(t *testing.T) {
	for name, frame := range map[string]collectiveFrame{
		"chunk": testChunkFrame(),
		"abort": {kind: collectiveKindAbort, senderRank: 1, failedRank: 2,
			session: testSession, tenant: "tenant-a", reason: reasonUnreachable},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := encodeCollectiveFrame(frame)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := decodeCollectiveFrame(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.kind != frame.kind || got.phase != frame.phase || got.op != frame.op ||
				got.senderRank != frame.senderRank || got.step != frame.step ||
				got.chunk != frame.chunk || got.failedRank != frame.failedRank ||
				got.session != frame.session || got.tenant != frame.tenant ||
				got.reason != frame.reason || got.total != frame.total ||
				len(got.values) != len(frame.values) {
				t.Fatalf("round trip changed the frame:\n got %+v\nwant %+v", got, frame)
			}
			for i, v := range frame.values {
				if got.values[i] != v {
					t.Fatalf("values[%d] = %v, want %v", i, got.values[i], v)
				}
			}
		})
	}
}

// The allocation guard, stated as a test: a frame may declare any count it likes,
// and the decoder must refuse it on the declared number and the bytes present
// rather than allocating first. A four-billion-element claim in a 90-byte frame
// must cost a comparison.
func TestCollectiveFrameRefusesAttackerChosenLengths(t *testing.T) {
	header := make([]byte, collectiveHeaderBytes)
	binary.BigEndian.PutUint32(header[0:4], collectiveMagic)
	header[4] = collectiveVersion
	header[5] = collectiveKindChunk
	header[6] = collectivePhaseReduceScatter
	header[7] = uint8(ReduceSum)
	copy(header[26:90], testSession)
	header[14] = uint8(len("tenant-a"))

	lie := func(mutate func(buf []byte) []byte) []byte {
		buf := append([]byte(nil), header...)
		buf = append(buf, "tenant-a"...)
		return mutate(buf)
	}
	cases := map[string][]byte{
		"count claims four billion values": lie(func(buf []byte) []byte {
			binary.BigEndian.PutUint32(buf[22:26], math.MaxUint32)
			binary.BigEndian.PutUint32(buf[18:22], math.MaxUint32)
			return buf
		}),
		"count above the value ceiling": lie(func(buf []byte) []byte {
			binary.BigEndian.PutUint32(buf[22:26], MaxCollectiveValues+1)
			binary.BigEndian.PutUint32(buf[18:22], MaxCollectiveValues+1)
			return buf
		}),
		"count exceeds the declared total": lie(func(buf []byte) []byte {
			binary.BigEndian.PutUint32(buf[22:26], 4)
			binary.BigEndian.PutUint32(buf[18:22], 2)
			return append(buf, make([]byte, 32)...)
		}),
		"payload shorter than the declared count": lie(func(buf []byte) []byte {
			binary.BigEndian.PutUint32(buf[22:26], 4)
			binary.BigEndian.PutUint32(buf[18:22], 4)
			return append(buf, make([]byte, 24)...)
		}),
		"trailing bytes after the payload": lie(func(buf []byte) []byte {
			binary.BigEndian.PutUint32(buf[22:26], 1)
			binary.BigEndian.PutUint32(buf[18:22], 1)
			return append(buf, make([]byte, 9)...)
		}),
		"tenant length runs past the frame": lie(func(buf []byte) []byte {
			buf[14] = 200
			return buf
		}),
		"header truncated": header[:collectiveHeaderBytes-1],
		"body above the frame ceiling": func() []byte {
			buf := make([]byte, maxCollectiveFrameBytes+1)
			binary.BigEndian.PutUint32(buf[0:4], collectiveMagic)
			buf[4] = collectiveVersion
			buf[5] = collectiveKindChunk
			return buf
		}(),
	}
	for name, buf := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCollectiveFrame(buf); err == nil {
				t.Fatal("decoder accepted an attacker-chosen length")
			}
		})
	}
}

func TestCollectiveFrameRefusesMalformedHeaders(t *testing.T) {
	valid, err := encodeCollectiveFrame(testChunkFrame())
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(buf []byte){
		"wrong magic":      func(buf []byte) { buf[0] ^= 0xff },
		"wrong version":    func(buf []byte) { buf[4] = collectiveVersion + 1 },
		"unknown kind":     func(buf []byte) { buf[5] = 9 },
		"unknown phase":    func(buf []byte) { buf[6] = 9 },
		"unknown op":       func(buf []byte) { buf[7] = 9 },
		"rank above bound": func(buf []byte) { binary.BigEndian.PutUint16(buf[8:10], MaxRingRanks) },
		"step above bound": func(buf []byte) { binary.BigEndian.PutUint16(buf[10:12], MaxRingRanks) },
		"session not hex":  func(buf []byte) { buf[30] = 'z' },
		"session upper":    func(buf []byte) { buf[30] = 'A' },
		"tenant control":   func(buf []byte) { buf[collectiveHeaderBytes] = '\n' },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			buf := append([]byte(nil), valid...)
			mutate(buf)
			if _, err := decodeCollectiveFrame(buf); err == nil {
				t.Fatalf("decoder accepted %s", name)
			}
		})
	}
}

// The encoder refuses what the decoder would refuse, so a bug produces a local
// error rather than a peer's HTTP status.
func TestCollectiveFrameEncoderRefusesWhatItCannotSend(t *testing.T) {
	cases := map[string]collectiveFrame{
		"no tenant": func() collectiveFrame {
			f := testChunkFrame()
			f.tenant = ""
			return f
		}(),
		"tenant too long": func() collectiveFrame {
			f := testChunkFrame()
			f.tenant = strings.Repeat("a", maxCollectiveTenant+1)
			return f
		}(),
		"session not a digest": func() collectiveFrame {
			f := testChunkFrame()
			f.session = "short"
			return f
		}(),
		"rank above the ring bound": func() collectiveFrame {
			f := testChunkFrame()
			f.senderRank = MaxRingRanks
			return f
		}(),
		"empty chunk": func() collectiveFrame {
			f := testChunkFrame()
			f.values = nil
			return f
		}(),
		"values above the declared total": func() collectiveFrame {
			f := testChunkFrame()
			f.total = 1
			return f
		}(),
		"chunk carrying a reason": func() collectiveFrame {
			f := testChunkFrame()
			f.reason = reasonProtocol
			return f
		}(),
		"abort carrying values": {kind: collectiveKindAbort, session: testSession,
			tenant: "tenant-a", reason: reasonProtocol, total: 1, values: []float64{1}},
		"abort without a reason": {kind: collectiveKindAbort, session: testSession, tenant: "tenant-a"},
		"oversized payload": func() collectiveFrame {
			f := testChunkFrame()
			f.total = MaxCollectiveValues + 1
			f.values = make([]float64, MaxCollectiveValues+1)
			return f
		}(),
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := encodeCollectiveFrame(frame); err == nil {
				t.Fatalf("encoder produced %s", name)
			}
		})
	}
}

// A full-size vector must fit the frame ceiling, or the bound would refuse the
// largest legitimate collective rather than only hostile ones.
func TestCollectiveFrameFitsAFullSizeChunk(t *testing.T) {
	frame := testChunkFrame()
	frame.total = MaxCollectiveValues
	frame.values = make([]float64, MaxCollectiveValues)
	encoded, err := encodeCollectiveFrame(frame)
	if err != nil {
		t.Fatalf("a full-size chunk was refused: %v", err)
	}
	if len(encoded) > maxCollectiveFrameBytes {
		t.Fatalf("encoded %d bytes, ceiling is %d", len(encoded), maxCollectiveFrameBytes)
	}
	if _, err := decodeCollectiveFrame(encoded); err != nil {
		t.Fatalf("a full-size chunk did not decode: %v", err)
	}
}
