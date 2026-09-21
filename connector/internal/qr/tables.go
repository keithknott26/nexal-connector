package qr

// The tables in this file are the only values in internal/qr that cannot be
// derived: every other constant (total codeword count, generator polynomials,
// format and version bit strings) is COMPUTED from the standard's rules in
// galois.go and matrix.go rather than transcribed, because a mistyped table
// entry produces a code that scans as garbage rather than failing a build.
//
// Provenance: ISO/IEC 18004 Table 9 (error correction characteristics) and
// Annex E (alignment pattern centres), level M only — level M is the one level
// this package emits, so the other three rows are deliberately absent instead
// of present and untested. The rows were mechanically transcribed from the
// widely used python `qrcode` package's own tables (an independent
// implementation of the same standard) rather than typed by hand, and
// TestTotalCodewordsMatchLayout re-derives `total` for all 40 versions from the
// function-pattern layout this package builds, so a wrong row fails the tests.

// blockPlan is one version's level-M error correction structure. group1 blocks
// carry data1 data codewords each and group2 blocks carry data2; group1 is
// always the SHORTER group, which is the order the interleaver requires.
type blockPlan struct {
	ecPerBlock, group1, data1, group2, data2, total int
}

func (p blockPlan) dataCodewords() int { return p.group1*p.data1 + p.group2*p.data2 }

// versionM[v-1] is the ISO/IEC 18004 Table 9 row for error correction
// level M at that version.
var versionM = [40]blockPlan{
	{ecPerBlock: 10, group1: 1, data1: 16, group2: 0, data2: 0, total: 26},      // version 1
	{ecPerBlock: 16, group1: 1, data1: 28, group2: 0, data2: 0, total: 44},      // version 2
	{ecPerBlock: 26, group1: 1, data1: 44, group2: 0, data2: 0, total: 70},      // version 3
	{ecPerBlock: 18, group1: 2, data1: 32, group2: 0, data2: 0, total: 100},     // version 4
	{ecPerBlock: 24, group1: 2, data1: 43, group2: 0, data2: 0, total: 134},     // version 5
	{ecPerBlock: 16, group1: 4, data1: 27, group2: 0, data2: 0, total: 172},     // version 6
	{ecPerBlock: 18, group1: 4, data1: 31, group2: 0, data2: 0, total: 196},     // version 7
	{ecPerBlock: 22, group1: 2, data1: 38, group2: 2, data2: 39, total: 242},    // version 8
	{ecPerBlock: 22, group1: 3, data1: 36, group2: 2, data2: 37, total: 292},    // version 9
	{ecPerBlock: 26, group1: 4, data1: 43, group2: 1, data2: 44, total: 346},    // version 10
	{ecPerBlock: 30, group1: 1, data1: 50, group2: 4, data2: 51, total: 404},    // version 11
	{ecPerBlock: 22, group1: 6, data1: 36, group2: 2, data2: 37, total: 466},    // version 12
	{ecPerBlock: 22, group1: 8, data1: 37, group2: 1, data2: 38, total: 532},    // version 13
	{ecPerBlock: 24, group1: 4, data1: 40, group2: 5, data2: 41, total: 581},    // version 14
	{ecPerBlock: 24, group1: 5, data1: 41, group2: 5, data2: 42, total: 655},    // version 15
	{ecPerBlock: 28, group1: 7, data1: 45, group2: 3, data2: 46, total: 733},    // version 16
	{ecPerBlock: 28, group1: 10, data1: 46, group2: 1, data2: 47, total: 815},   // version 17
	{ecPerBlock: 26, group1: 9, data1: 43, group2: 4, data2: 44, total: 901},    // version 18
	{ecPerBlock: 26, group1: 3, data1: 44, group2: 11, data2: 45, total: 991},   // version 19
	{ecPerBlock: 26, group1: 3, data1: 41, group2: 13, data2: 42, total: 1085},  // version 20
	{ecPerBlock: 26, group1: 17, data1: 42, group2: 0, data2: 0, total: 1156},   // version 21
	{ecPerBlock: 28, group1: 17, data1: 46, group2: 0, data2: 0, total: 1258},   // version 22
	{ecPerBlock: 28, group1: 4, data1: 47, group2: 14, data2: 48, total: 1364},  // version 23
	{ecPerBlock: 28, group1: 6, data1: 45, group2: 14, data2: 46, total: 1474},  // version 24
	{ecPerBlock: 28, group1: 8, data1: 47, group2: 13, data2: 48, total: 1588},  // version 25
	{ecPerBlock: 28, group1: 19, data1: 46, group2: 4, data2: 47, total: 1706},  // version 26
	{ecPerBlock: 28, group1: 22, data1: 45, group2: 3, data2: 46, total: 1828},  // version 27
	{ecPerBlock: 28, group1: 3, data1: 45, group2: 23, data2: 46, total: 1921},  // version 28
	{ecPerBlock: 28, group1: 21, data1: 45, group2: 7, data2: 46, total: 2051},  // version 29
	{ecPerBlock: 28, group1: 19, data1: 47, group2: 10, data2: 48, total: 2185}, // version 30
	{ecPerBlock: 28, group1: 2, data1: 46, group2: 29, data2: 47, total: 2323},  // version 31
	{ecPerBlock: 28, group1: 10, data1: 46, group2: 23, data2: 47, total: 2465}, // version 32
	{ecPerBlock: 28, group1: 14, data1: 46, group2: 21, data2: 47, total: 2611}, // version 33
	{ecPerBlock: 28, group1: 14, data1: 46, group2: 23, data2: 47, total: 2761}, // version 34
	{ecPerBlock: 28, group1: 12, data1: 47, group2: 26, data2: 48, total: 2876}, // version 35
	{ecPerBlock: 28, group1: 6, data1: 47, group2: 34, data2: 48, total: 3034},  // version 36
	{ecPerBlock: 28, group1: 29, data1: 46, group2: 14, data2: 47, total: 3196}, // version 37
	{ecPerBlock: 28, group1: 13, data1: 46, group2: 32, data2: 47, total: 3362}, // version 38
	{ecPerBlock: 28, group1: 40, data1: 47, group2: 7, data2: 48, total: 3532},  // version 39
	{ecPerBlock: 28, group1: 18, data1: 47, group2: 31, data2: 48, total: 3706}, // version 40
}

// alignmentCenters[v-1] lists the row/column centre coordinates of the
// alignment patterns for that version (ISO/IEC 18004 Annex E). Version 1
// has none.
var alignmentCenters = [40][]int{
	0:  nil,
	1:  {6, 18},
	2:  {6, 22},
	3:  {6, 26},
	4:  {6, 30},
	5:  {6, 34},
	6:  {6, 22, 38},
	7:  {6, 24, 42},
	8:  {6, 26, 46},
	9:  {6, 28, 50},
	10: {6, 30, 54},
	11: {6, 32, 58},
	12: {6, 34, 62},
	13: {6, 26, 46, 66},
	14: {6, 26, 48, 70},
	15: {6, 26, 50, 74},
	16: {6, 30, 54, 78},
	17: {6, 30, 56, 82},
	18: {6, 30, 58, 86},
	19: {6, 34, 62, 90},
	20: {6, 28, 50, 72, 94},
	21: {6, 26, 50, 74, 98},
	22: {6, 30, 54, 78, 102},
	23: {6, 28, 54, 80, 106},
	24: {6, 32, 58, 84, 110},
	25: {6, 30, 58, 86, 114},
	26: {6, 34, 62, 90, 118},
	27: {6, 26, 50, 74, 98, 122},
	28: {6, 30, 54, 78, 102, 126},
	29: {6, 26, 52, 78, 104, 130},
	30: {6, 30, 56, 82, 108, 134},
	31: {6, 34, 60, 86, 112, 138},
	32: {6, 30, 58, 86, 114, 142},
	33: {6, 34, 62, 90, 118, 146},
	34: {6, 30, 54, 78, 102, 126, 150},
	35: {6, 24, 50, 76, 102, 128, 154},
	36: {6, 28, 54, 80, 106, 132, 158},
	37: {6, 32, 58, 84, 110, 136, 162},
	38: {6, 26, 54, 82, 110, 138, 166},
	39: {6, 30, 58, 86, 114, 142, 170},
}
