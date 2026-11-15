package sandbox

import (
	"strings"
	"testing"
)

// GitCloneArgs is the hardening boundary for cloning untrusted repositories:
// hooks off, only https, no submodules, and the URL after "--" so it can never
// be parsed as an option.
func TestGitCloneArgsHardening(t *testing.T) {
	url := "https://example.com/r.git"
	a := GitCloneArgs(url, "/dest")
	joined := " " + strings.Join(a, " ") + " "
	for _, want := range []string{
		" core.hooksPath=/dev/null ", " protocol.allow=never ", " protocol.https.allow=always ",
		" core.symlinks=false ", " --depth 1 ", " --single-branch ", " --no-recurse-submodules ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("GitCloneArgs missing %q in %v", want, a)
		}
	}
	n := len(a)
	if a[n-3] != "--" || a[n-2] != url || a[n-1] != "/dest" {
		t.Errorf("GitCloneArgs must end with -- <url> <dest>, got %v", a[n-3:])
	}
	// An option-looking URL stays positional, after "--".
	b := GitCloneArgs("--upload-pack=evil", "/d")
	for i, x := range b {
		if x == "--upload-pack=evil" && b[i-1] != "--" {
			t.Errorf("option-like URL not preceded by --: %v", b)
		}
	}
}

func TestSidecarNameAndVolume(t *testing.T) {
	if got := SidecarName("ws1"); got != "nexal-mesh-ws1" {
		t.Errorf("SidecarName = %q", got)
	}
	if got := SidecarVolume("ws1"); got != "nexal-mesh-ws1" {
		t.Errorf("SidecarVolume = %q", got)
	}
}
