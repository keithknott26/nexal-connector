package qr

import "strings"

// QuietZone is the four-module light margin ISO/IEC 18004 §9.1 requires around a
// symbol. It is not decoration: a scanner locates the symbol by finding the
// finder patterns against an empty margin, and a code printed flush against
// surrounding text frequently will not acquire at all.
const QuietZone = 4

// Style selects a terminal rendering.
type Style struct {
	// Quiet is the margin in modules. Callers should leave it zero, which means
	// QuietZone; a smaller value is accepted for tests but weakens acquisition.
	Quiet int
	// ASCII renders with '#' and ' ' instead of Unicode block glyphs, for a
	// terminal or a log pipeline that cannot represent U+2580..U+2588.
	ASCII bool
	// Invert swaps ink and paper. The default draws DARK modules as ink, which
	// is correct on a light terminal background. On a dark background the
	// unmodified rendering is a photographic negative, and most scanners reject
	// it, so a dark-terminal user needs this. There is no way to detect the
	// background colour from a pipe, which is why this is an explicit flag
	// rather than a guess.
	Invert bool
}

func (s Style) quiet() int {
	if s.Quiet <= 0 {
		return QuietZone
	}
	return s.Quiet
}

// Terminal renders the symbol as text.
//
// The Unicode form uses half-block glyphs so that TWO module rows share one text
// row. That is a geometry fix, not a cleverness: a terminal cell is about twice
// as tall as it is wide, so one module per cell produces a symbol stretched 2:1
// vertically, and a stretched symbol is a symbol a phone cannot decode reliably.
// Packing two module rows into one cell makes each module square. The ASCII form
// cannot subdivide a cell, so it spends TWO characters per module horizontally
// to reach the same aspect ratio.
func (c *Code) Terminal(s Style) string {
	quiet := s.quiet()
	// Work on a padded copy so the quiet zone is real modules rather than a
	// special case inside the glyph loop.
	size := c.Size + quiet*2
	dark := make([][]bool, size)
	for r := range dark {
		dark[r] = make([]bool, size)
	}
	for r := 0; r < c.Size; r++ {
		for col := 0; col < c.Size; col++ {
			dark[r+quiet][col+quiet] = c.Modules[r][col]
		}
	}
	ink := func(r, col int) bool {
		if r >= size {
			return s.Invert // padding row below the symbol is paper, inverted or not
		}
		if s.Invert {
			return !dark[r][col]
		}
		return dark[r][col]
	}
	var b strings.Builder
	if s.ASCII {
		for r := 0; r < size; r++ {
			for col := 0; col < size; col++ {
				if ink(r, col) {
					b.WriteString("##")
				} else {
					b.WriteString("  ")
				}
			}
			b.WriteByte('\n')
		}
		return b.String()
	}
	for r := 0; r < size; r += 2 {
		for col := 0; col < size; col++ {
			upper, lower := ink(r, col), ink(r+1, col)
			switch {
			case upper && lower:
				b.WriteRune('█')
			case upper:
				b.WriteRune('▀')
			case lower:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Rows returns the module matrix as one string of '0' and '1' per row, which is
// the form the CLI puts in its JSON and the Mac UI draws directly. A compact
// text form was chosen over a nested array because it is a third of the bytes
// and trivially checkable by eye in a JSON dump.
//
// The matrix IS the rendered QR, so it necessarily carries whatever the payload
// carries, including the pairing claim token. That is the same exposure as
// printing the code on screen, which is the point of the command; what must
// never happen is the token appearing as a readable field, in a log, or on disk.
func (c *Code) Rows() []string {
	out := make([]string, c.Size)
	for r := 0; r < c.Size; r++ {
		row := make([]byte, c.Size)
		for col := 0; col < c.Size; col++ {
			row[col] = '0'
			if c.Modules[r][col] {
				row[col] = '1'
			}
		}
		out[r] = string(row)
	}
	return out
}
