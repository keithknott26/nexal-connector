package cybersecurity

import (
	"bytes"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const StyleVersion = "style_v1"

type StyleVector [6]float64

var styleIdentifiers = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)

// styleVector is an explainable formatting summary, not an authorship model.
// Dimensions: tab-indentation share, indentation width, comment-line share,
// mean line length, underscored-identifier share, and blank-line share.
func styleVector(name string, data []byte) (string, StyleVector, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	if !contains(ext, ".py", ".js", ".ts", ".jsx", ".tsx", ".sh", ".rb", ".ps1") || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", StyleVector{}, false
	}
	lines := strings.Split(string(data), "\n")
	var v StyleVector
	nonempty := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			v[5]++
			continue
		}
		nonempty++
		if strings.HasPrefix(line, "\t") {
			v[0]++
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		v[1] += math.Min(float64(indent)/16, 1)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			v[2]++
		}
		v[3] += math.Min(float64(utf8.RuneCountInString(line))/120, 1)
	}
	if nonempty < 20 {
		return "", StyleVector{}, false
	}
	for i := 0; i < 4; i++ {
		v[i] /= float64(nonempty)
	}
	ids := styleIdentifiers.FindAll(data, -1)
	for _, id := range ids {
		if bytes.ContainsRune(id, '_') {
			v[4]++
		}
	}
	if len(ids) > 0 {
		v[4] /= float64(len(ids))
	}
	v[5] /= float64(len(lines))
	return ext, v, true
}
func styleDistance(a, b StyleVector) float64 {
	var d float64
	for i := range a {
		d += math.Abs(a[i] - b[i])
	}
	return d / float64(len(a))
}
