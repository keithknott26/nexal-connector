package qr

// Symbol construction: function patterns, codeword placement, format and
// version information, masking and mask selection. ISO/IEC 18004 §7.7–§7.9.

// Code is a finished QR symbol. Modules[row][col] is true for a dark module.
// The struct is deliberately plain data: the terminal renderer, the JSON the
// CLI emits and the Swift view all consume the SAME matrix, so there is exactly
// one encoder in the system and the Mac UI cannot drift from the CLI.
type Code struct {
	Version int
	Mask    int
	Size    int
	Modules [][]bool
}

// layout holds a symbol under construction. reserved marks every module that
// belongs to a function pattern or to the format/version areas, i.e. every
// module the codeword placement must skip.
type layout struct {
	size     int
	modules  [][]bool
	reserved [][]bool
}

func newLayout(version int) *layout {
	size := version*4 + 17
	l := &layout{size: size, modules: make([][]bool, size), reserved: make([][]bool, size)}
	for i := 0; i < size; i++ {
		l.modules[i] = make([]bool, size)
		l.reserved[i] = make([]bool, size)
	}
	l.functionPatterns(version)
	return l
}

func (l *layout) set(row, col int, dark, reserve bool) {
	l.modules[row][col] = dark
	if reserve {
		l.reserved[row][col] = true
	}
}

// functionPatterns draws the finders, separators, timing patterns, alignment
// patterns and the dark module, and reserves the format and version areas.
func (l *layout) functionPatterns(version int) {
	// Finder patterns with their separators. Drawing the 8x8 region including
	// the separator in one pass is why the bounds run -1..7.
	for _, origin := range [][2]int{{0, 0}, {0, l.size - 7}, {l.size - 7, 0}} {
		for dr := -1; dr <= 7; dr++ {
			for dc := -1; dc <= 7; dc++ {
				r, c := origin[0]+dr, origin[1]+dc
				if r < 0 || r >= l.size || c < 0 || c >= l.size {
					continue
				}
				// Concentric rings: 7x7 dark border, light ring, 3x3 dark core.
				inner := dr >= 0 && dr <= 6 && dc >= 0 && dc <= 6
				ring := dr == 0 || dr == 6 || dc == 0 || dc == 6
				core := dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4
				l.set(r, c, inner && (ring || core), true)
			}
		}
	}
	// Timing patterns: alternating modules along row 6 and column 6, starting
	// dark at the even coordinates.
	for i := 8; i < l.size-8; i++ {
		dark := i%2 == 0
		l.set(6, i, dark, true)
		l.set(i, 6, dark, true)
	}
	// Alignment patterns at every pair of centres, except the three pairs that
	// would collide with a finder pattern.
	centers := alignmentCenters[version-1]
	for _, r := range centers {
		for _, c := range centers {
			if (r <= 8 && c <= 8) || (r <= 8 && c >= l.size-9) || (r >= l.size-9 && c <= 8) {
				continue
			}
			for dr := -2; dr <= 2; dr++ {
				for dc := -2; dc <= 2; dc++ {
					edge := dr == -2 || dr == 2 || dc == -2 || dc == 2
					l.set(r+dr, c+dc, edge || (dr == 0 && dc == 0), true)
				}
			}
		}
	}
	// Format information areas, reserved now and filled after the mask is
	// chosen, plus the single always-dark module below the top-left finder.
	for i := 0; i < 9; i++ {
		if !l.reserved[8][i] {
			l.set(8, i, false, true)
		}
		if !l.reserved[i][8] {
			l.set(i, 8, false, true)
		}
	}
	for i := 0; i < 8; i++ {
		l.set(8, l.size-1-i, false, true)
		l.set(l.size-1-i, 8, false, true)
	}
	l.set(l.size-8, 8, true, true) // the dark module
	if version >= 7 {
		for i := 0; i < 18; i++ {
			l.set(l.size-11+i%3, i/3, false, true)
			l.set(i/3, l.size-11+i%3, false, true)
		}
	}
}

// capacityBits is the number of modules available to codewords. It is DERIVED
// from the layout rather than tabulated, so it cannot disagree with the patterns
// actually drawn above.
func (l *layout) capacityBits() int {
	n := 0
	for r := 0; r < l.size; r++ {
		for c := 0; c < l.size; c++ {
			if !l.reserved[r][c] {
				n++
			}
		}
	}
	return n
}

// placeCodewords walks the standard upward/downward two-module-wide column
// zigzag from the bottom-right corner, skipping column 6 (the vertical timing
// pattern) and every reserved module. Leftover modules stay light, which is what
// the standard's remainder bits are.
func (l *layout) placeCodewords(data []byte) {
	bit := 0
	next := func() bool {
		if bit >= len(data)*8 {
			return false
		}
		b := data[bit/8]>>(7-bit%8)&1 == 1
		bit++
		return b
	}
	upward := true
	for right := l.size - 1; right >= 0; right -= 2 {
		if right == 6 {
			right-- // column 6 is timing; the pair to its left continues the walk
		}
		for i := 0; i < l.size; i++ {
			row := i
			if upward {
				row = l.size - 1 - i
			}
			for _, col := range [2]int{right, right - 1} {
				if col < 0 || l.reserved[row][col] {
					continue
				}
				l.modules[row][col] = next()
			}
		}
		upward = !upward
	}
}

// formatBits computes the 15-bit format information for level M and the given
// mask: five data bits (level M is 00, followed by the three mask bits), ten
// BCH(15,5) check bits, the whole word XORed with 0x5412 so an all-zero format
// is never a valid one.
func formatBits(mask int) int {
	data := 0<<3 | mask // level M is 00
	value := data << 10
	for i := 14; i >= 10; i-- {
		if value&(1<<i) != 0 {
			value ^= 0x537 << (i - 10) // generator x^10+x^8+x^5+x^4+x^2+x+1
		}
	}
	return (data<<10 | value) ^ 0x5412
}

// versionBits computes the 18-bit version information for version >= 7: six
// version bits plus twelve BCH(18,6) check bits. There is no final XOR here,
// unlike the format information.
func versionBits(version int) int {
	value := version << 12
	for i := 17; i >= 12; i-- {
		if value&(1<<i) != 0 {
			value ^= 0x1f25 << (i - 12) // generator x^12+x^11+x^10+x^9+x^8+x^5+x^2+1
		}
	}
	return version<<12 | value
}

// writeFormatAndVersion fills the reserved areas. The format information is
// written TWICE, in the two positions the standard defines, so a symbol with a
// damaged corner still decodes.
func (l *layout) writeFormatAndVersion(version, mask int) {
	format := formatBits(mask)
	bit := func(i int) bool { return format>>i&1 == 1 }
	// ISO/IEC 18004 figure 25. Bit 0 is the least significant bit of the 15-bit
	// format word, and the two copies run in OPPOSITE directions — which is
	// exactly the detail that is easy to mirror by accident. A mirrored format
	// word still produces a plausible-looking symbol whose every module outside
	// the format areas is correct, so it cannot be caught by inspection; the
	// golden fixtures in testdata/ are what pin it.
	//
	// Vertical copy, down column 8: bits 0-5 at rows 0-5, bits 6-7 at rows 7-8
	// (row 6 is the timing pattern), bits 8-14 at the bottom rows.
	for i := 0; i < 15; i++ {
		switch {
		case i < 6:
			l.modules[i][8] = bit(i)
		case i < 8:
			l.modules[i+1][8] = bit(i)
		default:
			l.modules[l.size-15+i][8] = bit(i)
		}
	}
	// Horizontal copy, along row 8: bits 0-7 from the right edge leftwards, bit 8
	// at column 7, bits 9-14 from column 5 leftwards to column 0.
	for i := 0; i < 15; i++ {
		switch {
		case i < 8:
			l.modules[8][l.size-1-i] = bit(i)
		case i == 8:
			l.modules[8][7] = bit(i)
		default:
			l.modules[8][14-i] = bit(i)
		}
	}
	l.modules[l.size-8][8] = true // the dark module, restated after the writes
	if version >= 7 {
		v := versionBits(version)
		for i := 0; i < 18; i++ {
			on := v>>i&1 == 1
			l.modules[l.size-11+i%3][i/3] = on
			l.modules[i/3][l.size-11+i%3] = on
		}
	}
}

// maskBit reports whether the mask pattern inverts the module at row,col.
// The eight expressions are ISO/IEC 18004 Table 10 verbatim.
func maskBit(mask, row, col int) bool {
	switch mask {
	case 0:
		return (row+col)%2 == 0
	case 1:
		return row%2 == 0
	case 2:
		return col%3 == 0
	case 3:
		return (row+col)%3 == 0
	case 4:
		return (row/2+col/3)%2 == 0
	case 5:
		return row*col%2+row*col%3 == 0
	case 6:
		return (row*col%2+row*col%3)%2 == 0
	default:
		return ((row+col)%2+row*col%3)%2 == 0
	}
}

// applyMask inverts every unreserved module the pattern selects. Reserved
// modules are never masked, which is why reserved is kept after placement.
func (l *layout) applyMask(mask int) {
	for r := 0; r < l.size; r++ {
		for c := 0; c < l.size; c++ {
			if !l.reserved[r][c] && maskBit(mask, r, c) {
				l.modules[r][c] = !l.modules[r][c]
			}
		}
	}
}

// penalty scores a masked symbol by the four rules of ISO/IEC 18004 §7.8.3.3.
// Lower is better. The rules exist to avoid patterns a scanner would mistake
// for a finder pattern, and large uniform areas that make sampling ambiguous.
func penalty(m [][]bool) int {
	size := len(m)
	score := 0
	// Rule 1: runs of five or more same-coloured modules in a row or column.
	for i := 0; i < size; i++ {
		for _, line := range [2][]bool{row(m, i), column(m, i)} {
			run, prev := 1, line[0]
			for j := 1; j < size; j++ {
				if line[j] == prev {
					run++
					continue
				}
				if run >= 5 {
					score += 3 + run - 5
				}
				run, prev = 1, line[j]
			}
			if run >= 5 {
				score += 3 + run - 5
			}
		}
	}
	// Rule 2: every 2x2 block of one colour, counted per block rather than per
	// maximal region, which is what "m x n block" reduces to for the standard's
	// (m-1)(n-1) weighting.
	for r := 0; r < size-1; r++ {
		for c := 0; c < size-1; c++ {
			if m[r][c] == m[r][c+1] && m[r][c] == m[r+1][c] && m[r][c] == m[r+1][c+1] {
				score += 3
			}
		}
	}
	// Rule 3: the 1:1:3:1:1 finder-like ratio with four light modules on either
	// side, in both orientations.
	patterns := [2][11]bool{
		{true, false, true, true, true, false, true, false, false, false, false},
		{false, false, false, false, true, false, true, true, true, false, true},
	}
	for i := 0; i < size; i++ {
		for _, line := range [2][]bool{row(m, i), column(m, i)} {
			for j := 0; j+11 <= size; j++ {
				for _, p := range patterns {
					hit := true
					for k := 0; k < 11; k++ {
						if line[j+k] != p[k] {
							hit = false
							break
						}
					}
					if hit {
						score += 40
					}
				}
			}
		}
	}
	// Rule 4: deviation of the dark-module proportion from 50%.
	dark := 0
	for r := 0; r < size; r++ {
		for c := 0; c < size; c++ {
			if m[r][c] {
				dark++
			}
		}
	}
	percent := dark * 100 / (size * size)
	deviation := percent - 50
	if deviation < 0 {
		deviation = -deviation
	}
	score += deviation / 5 * 10
	return score
}

func row(m [][]bool, i int) []bool { return m[i] }

func column(m [][]bool, i int) []bool {
	out := make([]bool, len(m))
	for r := range m {
		out[r] = m[r][i]
	}
	return out
}
