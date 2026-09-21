package qr

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// A QR DECODER, in the test package only.
//
// WHAT IT PROVES AND WHAT IT DOES NOT. Round-tripping through a decoder written
// by the same author against the same understanding of the standard is a
// regression test, not a correctness proof: a decoder that shares a
// misunderstanding with the encoder agrees with it perfectly. The external
// evidence is in qr_test.go (published known-answer vectors and golden matrices
// from an independent encoder); this file exists to (a) catch regressions across
// hundreds of payload sizes cheaply, and (b) check the properties external
// fixtures cannot express, such as "the error correction codewords are real
// parity, not decoration".
//
// It deliberately reads the format information from the module positions
// DIRECTLY, rather than through the writer, and it verifies parity by evaluating
// syndromes rather than by re-running the encoder's division loop.

type decoded struct {
	version, mask int
	payload       []byte
}

// decodeSymbol reverses a symbol: format information, unmasking, codeword
// extraction, de-interleaving, parity check and byte-mode segment parsing. It
// does NOT correct errors, because correcting errors is exactly what would hide
// an encoder bug.
func decodeSymbol(modules [][]bool) (decoded, error) {
	size := len(modules)
	if size < 21 || (size-17)%4 != 0 {
		return decoded{}, fmt.Errorf("size %d is not a QR symbol", size)
	}
	version := (size - 17) / 4
	mask, err := readFormat(modules)
	if err != nil {
		return decoded{}, err
	}
	// Unmask into a copy, using a fresh layout purely for its reserved map.
	l := newLayout(version)
	plain := make([][]bool, size)
	for r := 0; r < size; r++ {
		plain[r] = make([]bool, size)
		for c := 0; c < size; c++ {
			plain[r][c] = modules[r][c]
			if !l.reserved[r][c] && maskBit(mask, r, c) {
				plain[r][c] = !plain[r][c]
			}
		}
	}
	// Read the codeword stream back out of the zigzag.
	plan := versionM[version-1]
	var bits []bool
	upward := true
	for right := size - 1; right >= 0; right -= 2 {
		if right == 6 {
			right--
		}
		for i := 0; i < size; i++ {
			row := i
			if upward {
				row = size - 1 - i
			}
			for _, col := range [2]int{right, right - 1} {
				if col < 0 || l.reserved[row][col] {
					continue
				}
				bits = append(bits, plain[row][col])
			}
		}
		upward = !upward
	}
	stream := make([]byte, plan.total)
	for i := 0; i < plan.total*8; i++ {
		if bits[i] {
			stream[i/8] |= 1 << (7 - i%8)
		}
	}
	// De-interleave, then check every block's parity by evaluating syndromes.
	blocks := deinterleave(plan, stream)
	var data []byte
	for i, block := range blocks {
		for _, s := range syndromes(block, plan.ecPerBlock) {
			if s != 0 {
				return decoded{}, fmt.Errorf("block %d fails its parity check", i)
			}
		}
		data = append(data, block[:len(block)-plan.ecPerBlock]...)
	}
	payload, err := readByteSegment(data, version)
	if err != nil {
		return decoded{}, err
	}
	return decoded{version: version, mask: mask, payload: payload}, nil
}

// readFormat reads both copies of the format information straight from their
// module positions and accepts a copy only if it is one of the 32 legal format
// words. Level M is required, because that is the only level this package emits
// and silently accepting another level would hide a wrong format word.
func readFormat(modules [][]bool) (int, error) {
	size := len(modules)
	read := func(positions [][2]int) int {
		value := 0
		for i, p := range positions {
			if modules[p[0]][p[1]] {
				value |= 1 << i
			}
		}
		return value
	}
	vertical := make([][2]int, 15)
	horizontal := make([][2]int, 15)
	for i := 0; i < 15; i++ {
		switch {
		case i < 6:
			vertical[i] = [2]int{i, 8}
		case i < 8:
			vertical[i] = [2]int{i + 1, 8}
		default:
			vertical[i] = [2]int{size - 15 + i, 8}
		}
		switch {
		case i < 8:
			horizontal[i] = [2]int{8, size - 1 - i}
		case i == 8:
			horizontal[i] = [2]int{8, 7}
		default:
			horizontal[i] = [2]int{8, 14 - i}
		}
	}
	for _, word := range []int{read(vertical), read(horizontal)} {
		for mask := 0; mask < 8; mask++ {
			if formatBits(mask) == word {
				return mask, nil
			}
		}
	}
	return 0, errors.New("no readable level M format information")
}

// deinterleave is the inverse of interleave: it rebuilds each block as its data
// codewords followed by its error correction codewords.
func deinterleave(plan blockPlan, stream []byte) [][]byte {
	sizes := make([]int, 0, plan.group1+plan.group2)
	for i := 0; i < plan.group1; i++ {
		sizes = append(sizes, plan.data1)
	}
	for i := 0; i < plan.group2; i++ {
		sizes = append(sizes, plan.data2)
	}
	blocks := make([][]byte, len(sizes))
	pos := 0
	for i := 0; i < plan.data2 || i < plan.data1; i++ {
		for b, n := range sizes {
			if i < n {
				blocks[b] = append(blocks[b], stream[pos])
				pos++
			}
		}
	}
	for i := 0; i < plan.ecPerBlock; i++ {
		for b := range blocks {
			blocks[b] = append(blocks[b], stream[pos])
			pos++
		}
	}
	return blocks
}

// readByteSegment parses the one segment this package writes and refuses
// anything else, including the multi-segment encodings a general decoder accepts.
func readByteSegment(data []byte, version int) ([]byte, error) {
	var bits []bool
	for _, b := range data {
		for i := 7; i >= 0; i-- {
			bits = append(bits, b>>i&1 == 1)
		}
	}
	consumed := 0
	take := func(n int) int {
		value := 0
		for i := 0; i < n; i++ {
			value <<= 1
			if bits[0] {
				value |= 1
			}
			bits = bits[1:]
			consumed++
		}
		return value
	}
	if mode := take(4); mode != 0b0100 {
		return nil, fmt.Errorf("mode indicator %04b is not byte mode", mode)
	}
	count := take(countBits(version))
	if count*8 > len(bits) {
		return nil, fmt.Errorf("character count %d exceeds the remaining %d bits", count, len(bits))
	}
	out := make([]byte, count)
	for i := range out {
		out[i] = byte(take(8))
	}
	// Whatever follows must be the terminator, the bits that pad to a codeword
	// boundary, and then the standard's alternating pad codewords. A decoder that
	// ignored the tail would not notice a padding bug, and a padding bug is
	// invisible to a scanner right up to the version where it overflows.
	terminator := 4
	if len(bits) < 4 {
		terminator = len(bits)
	}
	for i := 0; i < terminator; i++ {
		if take(1) != 0 {
			return nil, errors.New("terminator bits are not zero")
		}
	}
	for consumed%8 != 0 {
		if take(1) != 0 {
			return nil, errors.New("padding to the codeword boundary is not zero")
		}
	}
	for i := 0; len(bits) >= 8; i++ {
		want := 0xEC
		if i%2 == 1 {
			want = 0x11
		}
		if got := take(8); got != want {
			return nil, fmt.Errorf("pad codeword %d is %#02x, want %#02x", i, got, want)
		}
	}
	if len(bits) != 0 {
		return nil, fmt.Errorf("%d bits left over after padding", len(bits))
	}
	return out, nil
}

// TestRoundTripAcrossEveryVersion encodes and decodes at every version boundary
// and at a spread of sizes in between, so no version's block structure, count
// field width or remainder-bit handling goes unexercised.
func TestRoundTripAcrossEveryVersion(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	sizes := map[int]bool{1: true, MaxBytes: true}
	for version := 1; version <= 40; version++ {
		capacity := (versionM[version-1].dataCodewords()*8 - 4 - countBits(version)) / 8
		sizes[capacity] = true
		if capacity > 1 {
			sizes[capacity-1] = true
		}
		sizes[capacity/2+1] = true
	}
	for n := range sizes {
		data := make([]byte, n)
		rnd.Read(data)
		code, err := Encode(data)
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		got, err := decodeSymbol(code.Modules)
		if err != nil {
			t.Fatalf("%d bytes (version %d, mask %d): %v", n, code.Version, code.Mask, err)
		}
		if got.version != code.Version || got.mask != code.Mask {
			t.Fatalf("%d bytes: decoded version %d mask %d, encoded version %d mask %d",
				n, got.version, got.mask, code.Version, code.Mask)
		}
		if !bytes.Equal(got.payload, data) {
			t.Fatalf("%d bytes: payload did not survive the round trip", n)
		}
	}
}

// TestRoundTripOfPairingShapedPayloads uses payloads of the exact shape the
// pairing command encodes, including the largest plausible coordinator origin,
// because that is the only payload that matters in production.
func TestRoundTripOfPairingShapedPayloads(t *testing.T) {
	for _, origin := range []string{
		"https://coordinator-dev.nexal.systems",
		"https://a.very-long-coordinator-hostname.example.coordinator.nexal.systems:8443",
		"https://x.test",
	} {
		payload := fmt.Sprintf(`{"schemaVersion":1,"type":"nexal-device-pairing","coordinator":%q,`+
			`"pairingId":"3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b","expiresAt":"2026-09-21T19:04:00.000Z",`+
			`"role":"receiver","claimToken":%q}`, origin, strings.Repeat("7c9e6679", 8))
		code, err := Encode([]byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", origin, err)
		}
		got, err := decodeSymbol(code.Modules)
		if err != nil {
			t.Fatalf("%s: %v", origin, err)
		}
		if string(got.payload) != payload {
			t.Fatalf("%s: payload did not survive the round trip", origin)
		}
		// A pairing payload should never need a huge symbol; if it does, the phone
		// has to be held further away and acquisition gets worse.
		if code.Version > 16 {
			t.Errorf("%s: pairing payload needed version %d", origin, code.Version)
		}
	}
}

// TestErrorCorrectionCodewordsAreRealParity flips one data module at a time and
// requires the parity check to notice. Without this, an encoder that wrote
// plausible-looking but wrong EC codewords would pass every round-trip test,
// because the decoder here does not use them to correct anything.
func TestErrorCorrectionCodewordsAreRealParity(t *testing.T) {
	code, err := Encode([]byte(`{"type":"nexal-device-pairing","role":"donor"}`))
	if err != nil {
		t.Fatal(err)
	}
	l := newLayout(code.Version)
	flipped := 0
	for r := 0; r < code.Size && flipped < 40; r++ {
		for c := 0; c < code.Size && flipped < 40; c++ {
			if l.reserved[r][c] {
				continue
			}
			damaged := clone(code.Modules)
			damaged[r][c] = !damaged[r][c]
			if _, err := decodeSymbol(damaged); err == nil {
				t.Fatalf("flipping module (%d,%d) left every block's parity intact", r, c)
			}
			flipped++
		}
	}
	if flipped == 0 {
		t.Fatal("no data module was flipped")
	}
}

// TestFormatInformationIsWrittenTwice damages the first copy and requires the
// symbol to remain readable, which is the entire point of the second copy.
func TestFormatInformationIsWrittenTwice(t *testing.T) {
	code, err := Encode([]byte("pairing"))
	if err != nil {
		t.Fatal(err)
	}
	damaged := clone(code.Modules)
	for i := 0; i < 6; i++ {
		damaged[i][8] = !damaged[i][8]
	}
	got, err := decodeSymbol(damaged)
	if err != nil {
		t.Fatalf("a damaged first format copy made the symbol unreadable: %v", err)
	}
	if got.mask != code.Mask {
		t.Errorf("second format copy read mask %d, want %d", got.mask, code.Mask)
	}
}

// TestFunctionPatternsAreWhereTheStandardPutsThem checks the fixed structures by
// position rather than by round trip: a misplaced finder pattern or a missing
// dark module can still round-trip through a decoder that shares the mistake.
func TestFunctionPatternsAreWhereTheStandardPutsThem(t *testing.T) {
	for _, version := range []int{1, 7, 13, 40} {
		code, err := Encode(bytes.Repeat([]byte{'x'}, (versionM[version-1].dataCodewords()*8-4-countBits(version))/8))
		if err != nil {
			t.Fatal(err)
		}
		m, size := code.Modules, code.Size
		for _, origin := range [][2]int{{0, 0}, {0, size - 7}, {size - 7, 0}} {
			for dr := 0; dr < 7; dr++ {
				for dc := 0; dc < 7; dc++ {
					ring := dr == 0 || dr == 6 || dc == 0 || dc == 6
					core := dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4
					if got := m[origin[0]+dr][origin[1]+dc]; got != (ring || core) {
						t.Fatalf("version %d finder at %v module (%d,%d) = %v", version, origin, dr, dc, got)
					}
				}
			}
		}
		for i := 8; i < size-8; i++ {
			if m[6][i] != (i%2 == 0) || m[i][6] != (i%2 == 0) {
				t.Fatalf("version %d timing pattern wrong at %d", version, i)
			}
		}
		if !m[size-8][8] {
			t.Fatalf("version %d is missing the always-dark module", version)
		}
		// The quiet zone is the renderer's job, but the separator ring around each
		// finder is part of the symbol and must be light.
		for i := 0; i < 8; i++ {
			if m[7][i] || m[i][7] {
				t.Fatalf("version %d separator is dark at %d", version, i)
			}
		}
	}
}

func clone(m [][]bool) [][]bool {
	out := make([][]bool, len(m))
	for i := range m {
		out[i] = append([]bool(nil), m[i]...)
	}
	return out
}
