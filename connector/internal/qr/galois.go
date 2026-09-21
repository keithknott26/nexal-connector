package qr

// Reed-Solomon over GF(256), hand-rolled because the founder's rule for this
// connector is zero new dependencies and a QR encoder is the whole reason a
// dependency would have been added here.
//
// The field is the one ISO/IEC 18004 §7.5.2 fixes for QR: GF(2^8) with the
// primitive polynomial x^8 + x^4 + x^3 + x^2 + 1 (0x11d) and 2 as a generator.
// Nothing here is configurable, because a "flexible" field would be a field
// that can be wrong.

const primitive = 0x11d

// expTable[i] = 2^i and logTable[x] = i such that 2^i = x. Built once at init
// so multiplication is two lookups and an add rather than a bit loop.
var expTable [256]byte
var logTable [256]byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		expTable[i] = byte(x)
		logTable[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= primitive
		}
	}
	expTable[255] = expTable[0] // wrap, so exponents may be taken mod 255 loosely
}

// mul multiplies in GF(256). Zero is special-cased because it has no logarithm.
func mul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return expTable[(int(logTable[a])+int(logTable[b]))%255]
}

// generator returns the RS generator polynomial of degree n, that is
// (x - 2^0)(x - 2^1)...(x - 2^(n-1)), highest power first. Subtraction is XOR,
// so the sign the textbook formula carries does not appear here.
func generator(n int) []byte {
	g := []byte{1}
	for i := 0; i < n; i++ {
		// Multiply g by (x + 2^i).
		next := make([]byte, len(g)+1)
		for j, c := range g {
			next[j] ^= c                         // c * x
			next[j+1] ^= mul(c, expTable[i%255]) // c * 2^i
		}
		g = next
	}
	return g
}

// remainder returns the n error correction codewords for data: the remainder of
// data(x) * x^n divided by the degree-n generator polynomial. This is plain
// polynomial long division in GF(256), which is what the standard specifies.
func remainder(data []byte, n int) []byte {
	g := generator(n)
	// The working buffer holds the dividend followed by n zero terms.
	buf := make([]byte, len(data)+n)
	copy(buf, data)
	for i := 0; i < len(data); i++ {
		lead := buf[i]
		if lead == 0 {
			continue
		}
		for j, c := range g {
			buf[i+j] ^= mul(c, lead)
		}
	}
	return buf[len(data):]
}

// syndromes evaluates a received codeword block (data followed by its EC
// codewords) at 2^0..2^(n-1). A block with no errors yields all zeros; this is
// used by the tests to check parity independently of the encoder's own division
// loop, since re-running remainder() would only prove the code agrees with
// itself.
func syndromes(block []byte, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		x := expTable[i%255]
		var acc byte
		for _, c := range block { // Horner, highest power first
			acc = mul(acc, x) ^ c
		}
		out[i] = acc
	}
	return out
}
