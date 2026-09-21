package qr

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The renderer is the part a founder actually looks at, and a rendering bug
// (missing quiet zone, stretched modules, inverted polarity) produces a code that
// is present on screen and unscannable — the worst failure mode, because it looks
// finished. These tests assert the geometry rather than eyeballing it.

func testCode(t *testing.T) *Code {
	t.Helper()
	code, err := Encode([]byte(`{"type":"nexal-device-pairing","role":"receiver"}`))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestUnicodeRenderingPacksTwoModuleRowsPerLine(t *testing.T) {
	code := testCode(t)
	lines := strings.Split(strings.TrimRight(code.Terminal(Style{}), "\n"), "\n")
	padded := code.Size + 2*QuietZone
	// Two module rows per text line, rounded up, is what makes the modules square
	// in a terminal cell that is twice as tall as it is wide.
	if want := (padded + 1) / 2; len(lines) != want {
		t.Fatalf("%d lines for %d module rows, want %d", len(lines), padded, want)
	}
	for i, line := range lines {
		if got := utf8.RuneCountInString(line); got != padded {
			t.Fatalf("line %d has %d cells, want %d", i, got, padded)
		}
		for _, r := range line {
			switch r {
			case '█', '▀', '▄', ' ':
			default:
				t.Fatalf("line %d contains %q, which is not a half-block glyph", i, r)
			}
		}
	}
}

func TestQuietZoneIsFourModulesOnEverySide(t *testing.T) {
	code := testCode(t)
	lines := strings.Split(strings.TrimRight(code.Terminal(Style{ASCII: true}), "\n"), "\n")
	if len(lines) != code.Size+2*QuietZone {
		t.Fatalf("%d ASCII lines, want %d", len(lines), code.Size+2*QuietZone)
	}
	blank := strings.Repeat("  ", code.Size+2*QuietZone)
	for i := 0; i < QuietZone; i++ {
		if lines[i] != blank || lines[len(lines)-1-i] != blank {
			t.Fatalf("row %d is not part of an empty quiet zone", i)
		}
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, strings.Repeat("  ", QuietZone)) ||
			!strings.HasSuffix(line, strings.Repeat("  ", QuietZone)) {
			t.Fatal("a row is missing its left or right quiet zone")
		}
	}
}

// The ASCII fallback cannot subdivide a character cell vertically, so it must
// spend two characters per module horizontally to keep the symbol square.
func TestASCIIFallbackIsTwoCharactersPerModuleAndPureASCII(t *testing.T) {
	code := testCode(t)
	out := code.Terminal(Style{ASCII: true})
	for _, r := range out {
		if r > 127 {
			t.Fatalf("ASCII fallback emitted %q", r)
		}
	}
	line := strings.Split(out, "\n")[QuietZone+8]
	if len(line) != 2*(code.Size+2*QuietZone) {
		t.Fatalf("ASCII line is %d characters wide for %d modules", len(line), code.Size+2*QuietZone)
	}
}

// Invert exists because a symbol rendered ink-on-paper is a photographic negative
// on a dark terminal, and most scanners refuse a negative. The two renderings must
// therefore be exact complements of each other.
func TestInvertIsTheExactComplement(t *testing.T) {
	code := testCode(t)
	normal := code.Terminal(Style{ASCII: true})
	inverted := code.Terminal(Style{ASCII: true, Invert: true})
	if normal == inverted {
		t.Fatal("inverted rendering is identical to the normal one")
	}
	swapped := strings.NewReplacer("##", "\x00\x00", "  ", "##").Replace(inverted)
	swapped = strings.ReplaceAll(swapped, "\x00\x00", "  ")
	if swapped != normal {
		t.Fatal("inverted rendering is not the complement of the normal one")
	}
}

func TestRowsMatchTheModuleMatrix(t *testing.T) {
	code := testCode(t)
	rows := code.Rows()
	if len(rows) != code.Size {
		t.Fatalf("%d rows for a %d module symbol", len(rows), code.Size)
	}
	for r, row := range rows {
		if len(row) != code.Size {
			t.Fatalf("row %d is %d characters", r, len(row))
		}
		for c := 0; c < code.Size; c++ {
			want := byte('0')
			if code.Modules[r][c] {
				want = '1'
			}
			if row[c] != want {
				t.Fatalf("row %d column %d = %q", r, c, row[c])
			}
		}
	}
}
