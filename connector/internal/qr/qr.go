// Package qr encodes a byte string as a QR symbol at error correction level M
// and renders it for a terminal.
//
// WHY THIS EXISTS AT ALL: device pairing shows the phone a QR code, and the
// connector's standing rule is that everything shipping in this binary is read
// line by line — so a QR library was not an option. The scope is therefore
// deliberately the narrowest that serves the pairing payload and nothing more:
// BYTE MODE ONLY (the payload is JSON, so numeric and alphanumeric modes would
// be dead code), LEVEL M ONLY (the standard's default trade-off, and the level a
// phone camera at arm's length is designed around), the lowest version that
// fits, and all eight mask patterns scored by the standard's penalty rules.
//
// Kanji mode, mixed-mode segments, ECI, micro QR, structured append and error
// CORRECTION (as opposed to generation) are absent on purpose. A decoder lives
// in the test package only, where it exists to prove the encoder rather than to
// ship.
package qr

import (
	"errors"
	"fmt"
)

// MaxBytes is the largest payload this package will encode: version 40 at level
// M in byte mode. The pairing payload is roughly 260 bytes (version 12-13), so
// the limit exists to reject nonsense early rather than to be approached.
const MaxBytes = 2331

// Encode returns the smallest level-M QR symbol containing data, with the mask
// pattern that scores best under the standard's penalty rules.
func Encode(data []byte) (*Code, error) {
	if len(data) == 0 {
		return nil, errors.New("qr: refusing to encode an empty payload")
	}
	version, plan, err := chooseVersion(len(data))
	if err != nil {
		return nil, err
	}
	codewords := interleave(plan, encodeData(data, version, plan))
	// Every mask is built, scored and kept; the best-scoring symbol wins. This
	// is eight full placements rather than a heuristic because the placement is
	// cheap at these sizes and "pick mask 0" is how an unscannable code ships.
	var best *Code
	bestScore := 0
	for mask := 0; mask < 8; mask++ {
		l := newLayout(version)
		l.placeCodewords(codewords)
		l.applyMask(mask)
		l.writeFormatAndVersion(version, mask)
		score := penalty(l.modules)
		if best == nil || score < bestScore {
			best = &Code{Version: version, Mask: mask, Size: l.size, Modules: l.modules}
			bestScore = score
		}
	}
	return best, nil
}

// chooseVersion returns the lowest version whose level-M data capacity holds the
// mode indicator, the character count and n data bytes.
func chooseVersion(n int) (int, blockPlan, error) {
	if n > MaxBytes {
		return 0, blockPlan{}, fmt.Errorf("qr: payload of %d bytes exceeds the %d-byte level M capacity", n, MaxBytes)
	}
	for version := 1; version <= 40; version++ {
		plan := versionM[version-1]
		if bitsNeeded(n, version) <= plan.dataCodewords()*8 {
			return version, plan, nil
		}
	}
	return 0, blockPlan{}, fmt.Errorf("qr: payload of %d bytes does not fit any level M version", n)
}

// countBits is the width of the byte-mode character count field: 8 bits up to
// version 9, 16 bits from version 10 (ISO/IEC 18004 Table 3).
func countBits(version int) int {
	if version <= 9 {
		return 8
	}
	return 16
}

func bitsNeeded(n, version int) int { return 4 + countBits(version) + n*8 }

// encodeData produces the version's data codewords: the byte-mode segment, the
// terminator, padding to a codeword boundary and then the standard's alternating
// pad codewords 0xEC/0x11.
func encodeData(data []byte, version int, plan blockPlan) []byte {
	var b bitWriter
	b.write(0b0100, 4) // byte mode indicator
	b.write(len(data), countBits(version))
	for _, c := range data {
		b.write(int(c), 8)
	}
	capacity := plan.dataCodewords() * 8
	// Terminator: four zero bits, or fewer if the symbol is nearly full.
	terminator := 4
	if remaining := capacity - b.length(); remaining < 4 {
		terminator = remaining
	}
	b.write(0, terminator)
	b.write(0, (8-b.length()%8)%8) // pad to a whole codeword
	out := b.bytes()
	for i := 0; len(out) < plan.dataCodewords(); i++ {
		if i%2 == 0 {
			out = append(out, 0xEC)
		} else {
			out = append(out, 0x11)
		}
	}
	return out
}

// interleave splits the data codewords into the version's blocks, computes each
// block's error correction codewords and writes the interleaved stream the
// standard requires: data codeword i of every block in order, then EC codeword i
// of every block in order. Interleaving is what makes a localised smudge damage
// one codeword per block instead of destroying one block entirely.
func interleave(plan blockPlan, data []byte) []byte {
	blocks := make([][]byte, 0, plan.group1+plan.group2)
	ec := make([][]byte, 0, plan.group1+plan.group2)
	offset := 0
	add := func(count, size int) {
		for i := 0; i < count; i++ {
			block := data[offset : offset+size]
			offset += size
			blocks = append(blocks, block)
			ec = append(ec, remainder(block, plan.ecPerBlock))
		}
	}
	add(plan.group1, plan.data1)
	add(plan.group2, plan.data2)
	out := make([]byte, 0, plan.total)
	for i := 0; i < plan.data2 || i < plan.data1; i++ {
		for _, block := range blocks {
			if i < len(block) {
				out = append(out, block[i])
			}
		}
	}
	for i := 0; i < plan.ecPerBlock; i++ {
		for _, block := range ec {
			out = append(out, block[i])
		}
	}
	return out
}

// bitWriter accumulates a big-endian bit string. Nothing here is reusable
// beyond this package, which is why it is not exported.
type bitWriter struct {
	bits []bool
}

func (w *bitWriter) write(value, width int) {
	for i := width - 1; i >= 0; i-- {
		w.bits = append(w.bits, value>>i&1 == 1)
	}
}

func (w *bitWriter) length() int { return len(w.bits) }

func (w *bitWriter) bytes() []byte {
	out := make([]byte, (len(w.bits)+7)/8)
	for i, bit := range w.bits {
		if bit {
			out[i/8] |= 1 << (7 - i%8)
		}
	}
	return out
}
