package sandbox

import "testing"

func TestInsideTreeRejectsEscapesAndUnsafePaths(t *testing.T) {
	cases := []struct {
		name      string
		configDir string
		rel       string
		want      bool
	}{
		{"plain child", ".devcontainer", "Dockerfile", true},
		{"sibling via parent stays inside", ".devcontainer", "../Dockerfile", true},
		{"root config dir", ".", "Dockerfile", true},
		{"nested", "a/b", "../c/d", true},
		{"escapes root from root", ".", "../x", false},
		{"escapes root from nested", "a", "../../x", false},
		{"bare parent", "a", "..", true},
		{"bare parent from root", ".", "..", false},
		{"empty", ".devcontainer", "", false},
		{"absolute", ".devcontainer", "/etc/passwd", false},
		{"backslash", ".devcontainer", `..\x`, false},
		{"nul byte", ".devcontainer", "a\x00b", false},
	}
	for _, c := range cases {
		if got := insideTree(c.configDir, c.rel); got != c.want {
			t.Errorf("%s: insideTree(%q, %q) = %v, want %v", c.name, c.configDir, c.rel, got, c.want)
		}
	}
}
