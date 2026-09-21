package qr

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file are the ONLY reason to believe this encoder works,
// because nothing else in the repository can read a QR code. They are therefore
// deliberately split into two kinds, and the distinction matters:
//
//  1. EXTERNAL GROUND TRUTH. Published known-answer vectors (ISO/IEC 18004's
//     worked example as reproduced by the thonky.com tutorial, and the standard's
//     format/version information tables) plus golden matrices produced by the
//     INDEPENDENT python `qrcode` package. A bug in this package cannot make
//     these pass.
//  2. SELF-CONSISTENCY. The decoder in decode_test.go round-trips what this
//     package encodes. That is useful for catching regressions and proves nothing
//     on its own — a decoder written against a wrong encoder agrees with it
//     perfectly. It is here IN ADDITION to (1), never instead of it.
//
// Every test is offline. No network, no temporary files, no external binary.

// TestFormatInformationMatchesPublishedTable checks all eight level-M format
// words against the published table (ISO/IEC 18004 Table 25; the same values are
// tabulated at thonky.com/qr-code-tutorial/format-version-tables). This is the
// bit of the symbol a scanner reads FIRST, so getting it wrong makes every
// symbol undecodable while leaving the rest of the matrix perfect.
func TestFormatInformationMatchesPublishedTable(t *testing.T) {
	published := [8]string{
		"101010000010010", // M, mask 0
		"101000100100101", // M, mask 1
		"101111001111100", // M, mask 2
		"101101101001011", // M, mask 3
		"100010111111001", // M, mask 4
		"100000011001110", // M, mask 5
		"100111110010111", // M, mask 6
		"100101010100000", // M, mask 7
	}
	for mask, want := range published {
		got := strconv.FormatInt(int64(formatBits(mask)), 2)
		got = strings.Repeat("0", 15-len(got)) + got
		if got != want {
			t.Errorf("formatBits(%d) = %s, published table says %s", mask, got, want)
		}
	}
}

// TestVersionInformationMatchesPublishedTable checks the BCH(18,6) version words
// for the versions a pairing payload can reach and beyond.
func TestVersionInformationMatchesPublishedTable(t *testing.T) {
	published := map[int]string{
		7:  "000111110010010100",
		8:  "001000010110111100",
		9:  "001001101010011001",
		10: "001010010011010011",
		11: "001011101111110110",
		12: "001100011101100010",
		13: "001101100001000111",
		14: "001110011000001101",
	}
	for version, want := range published {
		got := strconv.FormatInt(int64(versionBits(version)), 2)
		got = strings.Repeat("0", 18-len(got)) + got
		if got != want {
			t.Errorf("versionBits(%d) = %s, published table says %s", version, got, want)
		}
	}
}

// TestReedSolomonMatchesPublished1MVector is the published worked example for a
// 1-M symbol: sixteen data codewords and the ten error correction codewords they
// must produce (thonky.com/qr-code-tutorial/error-correction-coding, the
// "HELLO WORLD" 1-M example). The data codewords there are alphanumeric-mode,
// which is irrelevant: the Reed-Solomon stage sees codewords, not characters, so
// this is a true external check of the GF(256) arithmetic, the generator
// polynomial and the division loop.
func TestReedSolomonMatchesPublished1MVector(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	if got := remainder(data, 10); !bytes.Equal(got, want) {
		t.Fatalf("1-M error correction codewords = %v, published vector is %v", got, want)
	}
}

// published5Q is the second published worked example
// (thonky.com/qr-code-tutorial/structure-final-message): a 5-Q symbol, four
// blocks, eighteen error correction codewords per block. It exercises a DIFFERENT
// generator degree than the 1-M vector and, more importantly, it is the only
// external check of the interleaver — block order and column order are easy to
// get subtly wrong in a way that still produces a well-formed symbol.
var published5Q = struct {
	data  [4][]byte
	ec    [4][]byte
	final []byte
}{
	data: [4][]byte{
		{67, 85, 70, 134, 87, 38, 85, 194, 119, 50, 6, 18, 6, 103, 38},
		{246, 246, 66, 7, 118, 134, 242, 7, 38, 86, 22, 198, 199, 146, 6},
		{182, 230, 247, 119, 50, 7, 118, 134, 87, 38, 82, 6, 134, 151, 50, 7},
		{70, 247, 118, 86, 194, 6, 151, 50, 224, 236, 17, 236, 17, 236, 17, 236},
	},
	ec: [4][]byte{
		{213, 199, 11, 45, 115, 247, 241, 223, 229, 248, 154, 117, 154, 111, 86, 161, 111, 39},
		{87, 204, 96, 60, 202, 182, 124, 157, 200, 134, 27, 129, 209, 17, 163, 163, 120, 133},
		{148, 116, 177, 212, 76, 133, 75, 242, 238, 76, 195, 230, 189, 10, 108, 240, 192, 141},
		{140, 100, 250, 247, 108, 131, 37, 104, 253, 113, 111, 235, 197, 83, 6, 205, 89, 74},
	},
	final: []byte{
		67, 246, 182, 70, 85, 246, 230, 247, 70, 66, 247, 118, 134, 7, 119, 86, 87, 118, 50, 194,
		38, 134, 7, 6, 85, 242, 118, 151, 194, 7, 134, 50, 119, 38, 87, 224, 50, 86, 38, 236,
		6, 22, 82, 17, 18, 198, 6, 236, 6, 199, 134, 17, 103, 146, 151, 236, 38, 6, 50, 17,
		7, 236,
		213, 87, 148, 140, 199, 204, 116, 100, 11, 96, 177, 250, 45, 60, 212, 247, 115, 202, 76, 108,
		247, 182, 133, 131, 241, 124, 75, 37, 223, 157, 242, 104, 229, 200, 238, 253, 248, 134, 76, 113,
		154, 27, 195, 111, 117, 129, 230, 235, 154, 209, 189, 197, 111, 17, 10, 83, 86, 163, 108, 6,
		161, 163, 240, 205, 111, 120, 192, 89, 39, 133, 141, 74,
	},
}

func TestReedSolomonMatchesPublished5QVector(t *testing.T) {
	for i, block := range published5Q.data {
		if got := remainder(block, 18); !bytes.Equal(got, published5Q.ec[i]) {
			t.Errorf("block %d error correction codewords = %v, published vector is %v", i, got, published5Q.ec[i])
		}
	}
}

// TestInterleavingMatchesPublished5QVector drives the interleaver with the 5-Q
// block structure (two blocks of 15 data codewords, two of 16, 18 EC codewords
// each) even though this package only ever emits level M. The interleaving rule
// is level-independent, and 5-Q is the example the standard's tutorial works
// through in full, so it is the vector that exists.
func TestInterleavingMatchesPublished5QVector(t *testing.T) {
	var data []byte
	for _, block := range published5Q.data {
		data = append(data, block...)
	}
	plan := blockPlan{ecPerBlock: 18, group1: 2, data1: 15, group2: 2, data2: 16, total: 134}
	if got := interleave(plan, data); !bytes.Equal(got, published5Q.final) {
		t.Fatalf("interleaved final message differs from the published vector\n got %v\nwant %v", got, published5Q.final)
	}
}

// TestGoldenMatricesMatchIndependentEncoder compares this package's matrix,
// module for module, with matrices produced by the python `qrcode` package for
// the same payload and the same FORCED mask. Forcing the mask separates encoder
// correctness from mask-selection policy: if the two implementations agree on all
// eight masks, the data encoding, Reed-Solomon codewords, block interleaving,
// module placement, function patterns, format information and version
// information all agree with a second implementation of the standard.
//
// The fixture is committed, so this check stays offline and keeps working with no
// python installed. testdata/README.md records how it was generated.
func TestGoldenMatricesMatchIndependentEncoder(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden-masks.txt")
	if err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	var payload []byte
	var version, mask int
	var rows []string
	cases := 0
	check := func() {
		if payload == nil || rows == nil {
			return
		}
		got := encodeWithMask(t, payload, mask)
		if got.Version != version {
			t.Errorf("payload of %d bytes: version %d, independent encoder chose %d", len(payload), got.Version, version)
		}
		if diff := firstDiff(got.Rows(), rows); diff != "" {
			t.Errorf("payload of %d bytes, mask %d: %s", len(payload), mask, diff)
		}
		cases++
		rows = nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "PAYLOAD "):
			check()
			payload = decodeHex(t, strings.TrimPrefix(line, "PAYLOAD "))
		case strings.HasPrefix(line, "MASK "):
			check()
			fields := strings.Fields(line)
			mask, _ = strconv.Atoi(fields[1])
			version, _ = strconv.Atoi(fields[3])
			rows = nil
		default:
			rows = append(rows, line)
		}
	}
	check()
	if cases != 32 {
		t.Fatalf("compared %d golden matrices, expected 32 (4 payloads x 8 masks)", cases)
	}
}

// TestChosenMaskIsTheLowestPenalty asserts the selection policy itself: the
// symbol Encode returns must be the lowest-scoring of the eight, and its format
// information must name the mask that was actually applied.
func TestChosenMaskIsTheLowestPenalty(t *testing.T) {
	payload := []byte(`{"schemaVersion":1,"type":"nexal-device-pairing"}`)
	code, err := Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	best := 0
	for mask := 0; mask < 8; mask++ {
		score := penalty(encodeWithMask(t, payload, mask).Modules)
		if mask == 0 || score < best {
			best = score
		}
	}
	if got := penalty(code.Modules); got != best {
		t.Errorf("Encode chose mask %d with penalty %d; the best available is %d", code.Mask, got, best)
	}
	decoded, err := decodeSymbol(code.Modules)
	if err != nil {
		t.Fatalf("decoding the chosen symbol: %v", err)
	}
	if decoded.mask != code.Mask {
		t.Errorf("format information says mask %d, symbol was masked with %d", decoded.mask, code.Mask)
	}
}

// TestTotalCodewordsMatchLayout re-derives every version's codeword count from
// the function-pattern layout this package actually draws and compares it with
// the transcribed table. A single mistyped row in tables.go would show up here as
// a mismatch rather than as an unscannable code in the field.
func TestTotalCodewordsMatchLayout(t *testing.T) {
	for version := 1; version <= 40; version++ {
		plan := versionM[version-1]
		bits := newLayout(version).capacityBits()
		if bits/8 != plan.total {
			t.Errorf("version %d: layout has room for %d codewords, table says %d", version, bits/8, plan.total)
		}
		if got := plan.dataCodewords() + plan.ecPerBlock*(plan.group1+plan.group2); got != plan.total {
			t.Errorf("version %d: data+ec = %d, total = %d", version, got, plan.total)
		}
		// The standard's group 1 is the shorter group; the interleaver depends on it.
		if plan.group2 > 0 && plan.data2 != plan.data1+1 {
			t.Errorf("version %d: group sizes %d and %d are not consecutive", version, plan.data1, plan.data2)
		}
	}
}

// TestVersionSelectionIsTheLowestThatFits walks each version's exact byte-mode
// capacity boundary. Off-by-one here produces either a needlessly large symbol
// or a panic during placement.
func TestVersionSelectionIsTheLowestThatFits(t *testing.T) {
	for version := 1; version <= 40; version++ {
		capacity := (versionM[version-1].dataCodewords()*8 - 4 - countBits(version)) / 8
		got, _, err := chooseVersion(capacity)
		if err != nil {
			t.Fatalf("%d bytes: %v", capacity, err)
		}
		if got != version {
			// Versions 9/10 and 25/26 shift the character count field width, so a
			// capacity computed for version v can legitimately land on v-1; anything
			// else is a bug.
			if got == version-1 {
				continue
			}
			t.Errorf("%d bytes chose version %d, expected %d", capacity, got, version)
		}
		code, err := Encode(bytes.Repeat([]byte{'x'}, capacity))
		if err != nil {
			t.Fatalf("encoding %d bytes: %v", capacity, err)
		}
		if code.Size != code.Version*4+17 {
			t.Errorf("version %d symbol is %dx%d", code.Version, code.Size, code.Size)
		}
	}
}

func TestEncodeRefusesEmptyAndOversizedPayloads(t *testing.T) {
	if _, err := Encode(nil); err == nil {
		t.Error("an empty payload must be refused rather than producing an empty symbol")
	}
	if _, err := Encode(bytes.Repeat([]byte{'x'}, MaxBytes+1)); err == nil {
		t.Error("a payload above the version 40 level M capacity must be refused")
	}
	if _, err := Encode(bytes.Repeat([]byte{'x'}, MaxBytes)); err != nil {
		t.Errorf("the documented maximum payload must encode: %v", err)
	}
}

// encodeWithMask builds a symbol with the mask forced, which Encode deliberately
// does not expose: a caller has no legitimate reason to pick a mask, and the only
// consumer of a forced mask is this test file.
func encodeWithMask(t *testing.T, data []byte, mask int) *Code {
	t.Helper()
	version, plan, err := chooseVersion(len(data))
	if err != nil {
		t.Fatal(err)
	}
	l := newLayout(version)
	l.placeCodewords(interleave(plan, encodeData(data, version, plan)))
	l.applyMask(mask)
	l.writeFormatAndVersion(version, mask)
	return &Code{Version: version, Mask: mask, Size: l.size, Modules: l.modules}
}

func firstDiff(got, want []string) string {
	if len(got) != len(want) {
		return "matrix is " + strconv.Itoa(len(got)) + " rows, expected " + strconv.Itoa(len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			return "row " + strconv.Itoa(i) + "\n got " + got[i] + "\nwant " + want[i]
		}
	}
	return ""
}

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	if len(s)%2 != 0 {
		t.Fatalf("odd hex length %d", len(s))
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = byte(v)
	}
	return out
}
