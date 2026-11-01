package qr

import (
	"bytes"
	"testing"
)

// The field tables are the foundation every codeword rests on, so check them
// against the field axioms directly rather than only through encoded output.
func TestGF256TablesAreAConsistentField(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 255; i++ {
		e := expTable[i]
		if e == 0 || seen[e] {
			t.Fatalf("2^%d = %d is zero or repeats: 2 would not be a generator", i, e)
		}
		seen[e] = true
		if int(logTable[e]) != i {
			t.Fatalf("logTable[expTable[%d]] = %d, want %d", i, logTable[e], i)
		}
	}
	if expTable[255] != expTable[0] || expTable[0] != 1 {
		t.Fatalf("expTable wrap/identity wrong: exp[0]=%d exp[255]=%d", expTable[0], expTable[255])
	}
	// 2^8 reduces by the primitive polynomial: 0x100 ^ 0x11d = 0x1d.
	if expTable[8] != 0x1d {
		t.Fatalf("2^8 = %#x, want 0x1d", expTable[8])
	}
}

func TestGF256MulLaws(t *testing.T) {
	for a := 0; a < 256; a++ {
		if mul(byte(a), 0) != 0 || mul(0, byte(a)) != 0 {
			t.Fatalf("mul(%d,0) must be 0", a)
		}
		if mul(byte(a), 1) != byte(a) {
			t.Fatalf("mul(%d,1) != %d", a, a)
		}
		if a != 0 {
			// Every non-zero element has an inverse: a * a^-1 = 1.
			inv := expTable[(255-int(logTable[a]))%255]
			if mul(byte(a), inv) != 1 {
				t.Fatalf("%d has no inverse", a)
			}
		}
		for b := 0; b < 256; b += 7 {
			if mul(byte(a), byte(b)) != mul(byte(b), byte(a)) {
				t.Fatalf("mul not commutative for %d,%d", a, b)
			}
			c := byte(a ^ b*3)
			if mul(byte(a), byte(b)^c) != mul(byte(a), byte(b))^mul(byte(a), c) {
				t.Fatalf("mul not distributive over xor for %d,%d,%d", a, b, c)
			}
		}
	}
}

func TestGeneratorPolynomials(t *testing.T) {
	// (x+1)(x+2) = x^2 + 3x + 2 in GF(256).
	if got, want := generator(2), []byte{1, 3, 2}; !bytes.Equal(got, want) {
		t.Fatalf("generator(2) = %v, want %v", got, want)
	}
	// ISO/IEC 18004 Annex A: the degree-7 generator's coefficients as exponents
	// of 2 are 0, 87, 229, 146, 149, 238, 102, 21.
	exps := []int{0, 87, 229, 146, 149, 238, 102, 21}
	g := generator(7)
	if len(g) != len(exps) {
		t.Fatalf("generator(7) has %d terms, want %d", len(g), len(exps))
	}
	for i, e := range exps {
		if g[i] != expTable[e] {
			t.Fatalf("generator(7)[%d] = %d, want 2^%d = %d", i, g[i], e, expTable[e])
		}
	}
}
