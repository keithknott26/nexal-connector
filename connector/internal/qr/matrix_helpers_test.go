package qr

import "testing"

func filledMatrix(size int, dark func(r, c int) bool) [][]bool {
	m := make([][]bool, size)
	for r := range m {
		m[r] = make([]bool, size)
		for c := range m[r] {
			m[r][c] = dark(r, c)
		}
	}
	return m
}

// TestPenaltyHandComputedMatrices pins penalty against two matrices whose
// scores are worked out by hand from ISO/IEC 18004 §7.8.3.3.
func TestPenaltyHandComputedMatrices(t *testing.T) {
	const size = 21
	// All light: rule 1 sees 42 runs of 21 (3+16 each = 798); rule 2 sees
	// 20*20 uniform blocks (1200); rule 3 never matches; rule 4 sees 0% dark,
	// a 50-point deviation (50/5*10 = 100).
	if got, want := penalty(filledMatrix(size, func(r, c int) bool { return false })), 798+1200+100; got != want {
		t.Errorf("all-light penalty = %d, want %d", got, want)
	}
	// Checkerboard: no run of 5, no uniform 2x2 block, no finder-like ratio,
	// and 221/441 dark rounds down to 50%, so nothing is charged at all.
	if got := penalty(filledMatrix(size, func(r, c int) bool { return (r+c)%2 == 0 })); got != 0 {
		t.Errorf("checkerboard penalty = %d, want 0", got)
	}
}

// TestMaskBitSpotValues checks a few modules of each mask against
// ISO/IEC 18004 Table 10 worked by hand.
func TestMaskBitSpotValues(t *testing.T) {
	cases := []struct {
		mask, row, col int
		want           bool
	}{
		{0, 0, 0, true}, {0, 0, 1, false},
		{1, 0, 5, true}, {1, 1, 5, false},
		{2, 4, 3, true}, {2, 4, 4, false},
		{3, 1, 2, true}, {3, 1, 1, false},
		{4, 0, 0, true}, {4, 2, 0, false}, {4, 0, 3, false}, {4, 2, 3, true},
		{5, 0, 5, true}, {5, 1, 1, false}, {5, 1, 2, false},
		{6, 1, 1, true}, {6, 1, 3, false},
		{7, 0, 0, true}, {7, 1, 1, false}, {7, 2, 4, true},
	}
	for _, tc := range cases {
		if got := maskBit(tc.mask, tc.row, tc.col); got != tc.want {
			t.Errorf("maskBit(%d,%d,%d) = %v, want %v", tc.mask, tc.row, tc.col, got, tc.want)
		}
	}
}

func TestColumnReadsDownARow(t *testing.T) {
	m := [][]bool{{true, false}, {false, false}, {true, true}}
	got := column(m, 0)
	want := []bool{true, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("column(m,0) = %v, want %v", got, want)
		}
	}
	if len(column(m, 1)) != 3 {
		t.Errorf("column length = %d, want 3", len(column(m, 1)))
	}
}
